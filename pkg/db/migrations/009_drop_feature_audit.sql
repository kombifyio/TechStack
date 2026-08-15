-- 009_drop_feature_audit.sql
--
-- Settings surface reduction (ADR-0032): the local feature-flag management
-- API was removed; flag management is central (kombify Admin API). The
-- feature_audit table has no remaining writers (Service audit logger removed)
-- and no remaining readers (GET /api/v1/features/audit removed), so it is
-- dropped. feature_preferences and feature_consents stay: recorded per-user
-- preferences and consents keep gating reads in pkg/features.

DROP TABLE IF EXISTS feature_audit;
