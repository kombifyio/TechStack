-- The household worker projects members only onto stacks that run the
-- StackKits-managed Pocket ID issuer (platform-n3dcz). It reads that from the
-- stack's own service inventory: the identifying columns of a service row,
-- never its access, metadata or capabilities.
SET LOCAL lock_timeout = '5s';
GRANT SELECT(tenant_id,stack_id,service_key,name,management_state) ON public.services TO techstack_household_worker;
CREATE POLICY household_system_read ON public.services FOR SELECT TO techstack_household_worker USING(true);
