ALTER TABLE person ADD COLUMN daily_limit INTEGER; -- bytes; NULL = no daily limit

-- Daily history lives beside the period ledger: the period is the billing
-- window, the day is what a person sets a self-serve cap on.
CREATE TABLE IF NOT EXISTS daily_usage(
    day     TEXT NOT NULL, -- local YYYY-MM-DD
    subject TEXT NOT NULL,
    charged REAL NOT NULL DEFAULT 0,
    PRIMARY KEY(day, subject)
);
