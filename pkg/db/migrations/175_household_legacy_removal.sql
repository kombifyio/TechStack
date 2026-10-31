-- The household worker may also list a node's household accounts and remove
-- one legacy account that predates projection bindings (platform-nt9a1). Its
-- Go sender admits these two opcodes only under a retained attempt; every
-- other opcode stays closed to the worker here.
SET LOCAL lock_timeout = '5s';
ALTER POLICY household_system_commands ON typed_agent_commands
 USING(command_family='stackkit' AND command_json->>'operation' IN('STACKKIT_OPERATION_HOUSEHOLD_PROJECT','STACKKIT_OPERATION_HOUSEHOLD_LIST','STACKKIT_OPERATION_HOUSEHOLD_REMOVE'))
 WITH CHECK(command_family='stackkit' AND command_json->>'operation' IN('STACKKIT_OPERATION_HOUSEHOLD_PROJECT','STACKKIT_OPERATION_HOUSEHOLD_LIST','STACKKIT_OPERATION_HOUSEHOLD_REMOVE'));
ALTER POLICY household_worker_command_ceiling ON typed_agent_commands
 USING(session_user<>'techstack_household_worker' OR (command_family='stackkit' AND command_json->>'operation' IN('STACKKIT_OPERATION_HOUSEHOLD_PROJECT','STACKKIT_OPERATION_HOUSEHOLD_LIST','STACKKIT_OPERATION_HOUSEHOLD_REMOVE')))
 WITH CHECK(session_user<>'techstack_household_worker' OR (command_family='stackkit' AND command_json->>'operation' IN('STACKKIT_OPERATION_HOUSEHOLD_PROJECT','STACKKIT_OPERATION_HOUSEHOLD_LIST','STACKKIT_OPERATION_HOUSEHOLD_REMOVE')));
