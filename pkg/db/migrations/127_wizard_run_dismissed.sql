-- 127_wizard_run_dismissed.sql
--
-- The owner can dismiss a wizard run's dashboard notice. The dismissal is
-- server-side so it holds across devices and cleared browser storage. A keyed
-- retry that rewrites the row clears it again (see the wizard-run store).
ALTER TABLE wizard_runs ADD COLUMN IF NOT EXISTS dismissed_at timestamptz;
