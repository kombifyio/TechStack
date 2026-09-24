package providercontrol

import "fmt"

func validateExecutionAuthority(authority ExecutionAuthority) error {
	if authority == ExecutionAuthorityTechstackProviderControl {
		return nil
	}
	return fmt.Errorf("%w: unsupported authority %q", ErrExecutionAuthority, authority)
}

func requireExecutionAuthority(record OperationRecord, expected ExecutionAuthority) error {
	if err := validateExecutionAuthority(record.ExecutionAuthority); err != nil {
		return err
	}
	if record.ExecutionAuthority != expected {
		return fmt.Errorf(
			"%w: operation %q is bound to %q, not %q",
			ErrExecutionAuthority,
			record.Command.OperationID,
			record.ExecutionAuthority,
			expected,
		)
	}
	return nil
}
