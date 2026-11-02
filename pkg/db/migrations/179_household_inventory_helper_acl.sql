-- kombify:contract objects=public.techstack_household_inventory_owner,public.techstack_household_inventory_server
-- The backup worker shares Common identity verification, but never admits
-- household inventory requests. Keep the owner/target helpers runtime-only.
SET LOCAL lock_timeout = '5s';
REVOKE ALL ON FUNCTION public.techstack_household_inventory_owner(text,text),public.techstack_household_inventory_server(text,text,text) FROM techstack_backup_worker;
