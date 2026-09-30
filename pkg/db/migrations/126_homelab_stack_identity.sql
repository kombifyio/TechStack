-- 126_homelab_stack_identity.sql
--
-- One Stack Identity, one homelab name (STACK-IDENTITY-CUSTOMIZATION-STANDARD
-- §8). The homelab row is Techstack's local copy of the owner's Stack
-- Identity: `name` is the identity name, these columns carry the rest.
--
-- identity_presentation   character, animation, icon style and glow override;
--                         NULL until the identity was set with them.
-- identity_cloud_revision the kombify Cloud StackIdentityV1 revision this copy
--                         last matched; 0 means never linked to Cloud.
-- identity_pending        a local edit kombify Cloud has not confirmed yet.
--
-- named_at (migration 046) stays the time of the last identity edit, local or
-- adopted from Cloud; sync compares it with Cloud's updated_at.
ALTER TABLE homelabs ADD COLUMN IF NOT EXISTS identity_presentation jsonb;
ALTER TABLE homelabs ADD COLUMN IF NOT EXISTS identity_cloud_revision integer NOT NULL DEFAULT 0;
ALTER TABLE homelabs ADD COLUMN IF NOT EXISTS identity_pending boolean NOT NULL DEFAULT false;

-- A name the owner already chose locally is an unconfirmed local edit: the
-- first sync offers it to Cloud when Cloud has no identity yet.
UPDATE homelabs SET identity_pending = true
WHERE named_at IS NOT NULL AND identity_cloud_revision = 0;
