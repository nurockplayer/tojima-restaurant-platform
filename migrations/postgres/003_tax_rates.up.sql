CREATE TABLE tax_rate_schedule (
    tax_rule_id text PRIMARY KEY CHECK (length(btrim(tax_rule_id)) BETWEEN 1 AND 100),
    tax_class text NOT NULL CHECK (length(btrim(tax_class)) BETWEEN 1 AND 100),
    rate_numerator bigint NOT NULL CHECK (rate_numerator > 0),
    rate_denominator bigint NOT NULL CHECK (rate_denominator > 0),
    effective_from date NOT NULL,
    effective_to date,
    legal_source text NOT NULL CHECK (length(btrim(legal_source)) > 0),
    legal_status text NOT NULL CHECK (legal_status IN ('ENACTED', 'PROPOSED')),
    rule_version text NOT NULL CHECK (length(btrim(rule_version)) BETWEEN 1 AND 100),
    CHECK (effective_to IS NULL OR effective_to > effective_from)
);

-- The schedule is centralized legal data, not merchant-configurable rates.
-- Effective end dates are exclusive. The reduced rate is retained for future
-- eligible takeaway flows; this MVP's order scope remains dine-in only.
INSERT INTO tax_rate_schedule (
    tax_rule_id, tax_class, rate_numerator, rate_denominator,
    effective_from, effective_to, legal_source, legal_status, rule_version
) VALUES
    ('jp-standard-10-2019-10-01', 'JP_DINE_IN_FOOD', 10, 100,
     DATE '2019-10-01', NULL,
     'https://www.nta.go.jp/taxes/shiraberu/taxanswer/shohi/6303.htm',
     'ENACTED', 'jp-tax-schedule-2019-10-01-v1'),
    ('jp-reduced-8-2019-10-01', 'JP_ELIGIBLE_TAKEAWAY_FOOD', 8, 100,
     DATE '2019-10-01', NULL,
     'https://www.nta.go.jp/taxes/shiraberu/taxanswer/shohi/6303.htm; https://www.nta.go.jp/taxes/shiraberu/taxanswer/shohi/6102.htm',
     'ENACTED', 'jp-tax-schedule-2019-10-01-v1');
