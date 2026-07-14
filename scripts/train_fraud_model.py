#!/usr/bin/env python3
"""Train PayCore's fraud-risk model and export it as a language-neutral artifact.

This runs OFFLINE. It trains a small logistic-regression classifier on synthetic
charge data and writes internal/risk/model/fraud_model.json — the contract the Go
service reads. The Go side re-implements the exact same feature engineering and
scoring (standardize -> dot product -> sigmoid), so nothing about Python ships to
production; only this JSON does.

Reproduce with:  python3 scripts/train_fraud_model.py
"""

import json
import os
from datetime import datetime, timezone

import numpy as np
from sklearn.linear_model import LogisticRegression
from sklearn.metrics import precision_recall_fscore_support, roc_auc_score
from sklearn.model_selection import train_test_split
from sklearn.preprocessing import StandardScaler

SEED = 42
HOME_CURRENCY = "INR"
BLOCK_THRESHOLD = 0.5  # P(fraud) at or above which a charge is blocked
ARTIFACT_PATH = os.path.join(
    os.path.dirname(os.path.dirname(os.path.abspath(__file__))),
    "internal", "risk", "model", "fraud_model.json",
)

# Feature names, in the exact order the Go scorer must build them.
FEATURES = ["log_amount", "is_foreign", "log_amount_x_foreign"]


def make_features(amount_minor, is_foreign):
    """Build the feature vector from raw charge fields.

    Kept dead simple on purpose so Go can reproduce it byte-for-byte: a charge
    only carries amount + currency, so those are all we engineer from. amount is
    in minor units (paise/cents). The interaction term lets the model treat a
    foreign high-value charge as its own regime rather than the sum of two
    independent effects.
    """
    log_amount = np.log1p(amount_minor)
    return np.stack([log_amount, is_foreign, log_amount * is_foreign], axis=1)


def synthesize(n, rng):
    """Generate labeled charges. Labels come from a rule-with-noise that is NOT a
    logistic function, so recovering a good boundary is a real fit rather than a
    tautology."""
    # Amounts span a wide range: lognormal centered near a few hundred rupees.
    amount_minor = np.clip(rng.lognormal(mean=np.log(30000), sigma=1.3, size=n),
                           100, 10_000_00).astype(np.int64)
    is_foreign = (rng.random(n) < 0.30).astype(np.float64)

    # Ground-truth fraud propensity: big charges are riskier, foreign charges are
    # riskier, and foreign+big is riskier still (the interaction).
    p = np.full(n, 0.02)
    p += 0.15 * (amount_minor > 100_000)          # > 1000.00
    p += 0.22 * is_foreign
    p += 0.30 * (is_foreign * (amount_minor > 50_000))
    p += rng.normal(0, 0.05, size=n)              # noise: no clean boundary
    p = np.clip(p, 0, 0.98)
    label = (rng.random(n) < p).astype(np.int64)

    return amount_minor.astype(np.float64), is_foreign, label


def main():
    rng = np.random.default_rng(SEED)
    amount, is_foreign, y = synthesize(40_000, rng)
    X = make_features(amount, is_foreign)

    X_train, X_test, y_train, y_test = train_test_split(
        X, y, test_size=0.25, random_state=SEED, stratify=y)

    scaler = StandardScaler().fit(X_train)
    clf = LogisticRegression(max_iter=1000, class_weight="balanced")
    clf.fit(scaler.transform(X_train), y_train)

    prob = clf.predict_proba(scaler.transform(X_test))[:, 1]
    auc = roc_auc_score(y_test, prob)
    pred = (prob >= BLOCK_THRESHOLD).astype(np.int64)
    prec, rec, _, _ = precision_recall_fscore_support(
        y_test, pred, average="binary", zero_division=0)

    artifact = {
        "version": 1,
        "trained_at": datetime.now(timezone.utc).isoformat(),
        "home_currency": HOME_CURRENCY,
        "features": FEATURES,
        "mean": scaler.mean_.tolist(),
        "scale": scaler.scale_.tolist(),
        "coef": clf.coef_[0].tolist(),
        "intercept": float(clf.intercept_[0]),
        "block_threshold": BLOCK_THRESHOLD,
        "metrics": {
            "auc": round(float(auc), 4),
            "block_precision": round(float(prec), 4),
            "block_recall": round(float(rec), 4),
            "fraud_rate": round(float(y.mean()), 4),
            "n_train": int(len(y_train)),
            "n_test": int(len(y_test)),
        },
    }

    os.makedirs(os.path.dirname(ARTIFACT_PATH), exist_ok=True)
    with open(ARTIFACT_PATH, "w") as f:
        json.dump(artifact, f, indent=2)
        f.write("\n")

    print(f"wrote {ARTIFACT_PATH}")
    print(f"  test AUC          {auc:.4f}")
    print(f"  block precision   {prec:.4f}  (of charges blocked at p>={BLOCK_THRESHOLD}, how many were fraud)")
    print(f"  block recall      {rec:.4f}  (of actual fraud, how much we caught)")
    print(f"  base fraud rate   {y.mean():.4f}")

    # Sanity check: print scores for a few representative charges so the Go side
    # has known-good values to sanity-test against.
    print("\n  sample scores (probability of fraud):")
    for amt, foreign, label in [
        (50_000, 0, "500 domestic"),
        (200_000, 0, "2000 domestic"),
        (50_000, 1, "500 foreign"),
        (300_000, 1, "3000 foreign"),
    ]:
        feats = make_features(np.array([float(amt)]), np.array([float(foreign)]))
        pr = clf.predict_proba(scaler.transform(feats))[0, 1]
        verdict = "BLOCK" if pr >= BLOCK_THRESHOLD else "allow"
        print(f"    {label:<14} p={pr:.3f}  -> {verdict}")


if __name__ == "__main__":
    main()
