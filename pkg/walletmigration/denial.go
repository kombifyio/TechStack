package walletmigration

// Denial is a structured refusal in the platform reason envelope. The command
// prints it to stderr; it names settings and next steps, never a value.
type Denial struct {
	ErrorCode    string   `json:"error_code"`
	ReasonCode   string   `json:"reason_code"`
	Retryable    bool     `json:"retryable"`
	Message      string   `json:"message"`
	UserGuidance []string `json:"user_guidance"`
}

// Error implements error.
func (d *Denial) Error() string { return d.ErrorCode + ": " + d.Message }

// Error codes of the refusals that happen before the database is opened.
const (
	ErrorCodeWalletUnavailable     = "wallet_unavailable"
	ErrorCodeEncryptionKeyRequired = "encryption_key_required"
	ErrorCodeExecuteIsASubcommand  = "execute_is_a_subcommand"
)

// WalletUnavailable refuses execute because the kombify Wallet is not
// configured: moving rows into any other store is not an option. detail names
// the setting that is wrong and never a value.
func WalletUnavailable(detail string) *Denial {
	return &Denial{
		ErrorCode:  ErrorCodeWalletUnavailable,
		ReasonCode: "wallet_not_configured",
		Retryable:  false,
		Message:    "the kombify Wallet is not configured for this run (" + detail + "); nothing was read, decrypted or written",
		UserGuidance: []string{
			"Run execute with the hosted Wallet configuration: TECHSTACK_WALLET_SOURCE=kombify and the TECHSTACK_WALLET_* settings.",
			"Execute never falls back to the legacy store.",
		},
	}
}

// EncryptionKeyRequired refuses execute because the legacy key is missing or
// unusable: the enc:v1 values cannot be opened without it.
func EncryptionKeyRequired() *Denial {
	return &Denial{
		ErrorCode:  ErrorCodeEncryptionKeyRequired,
		ReasonCode: "legacy_key_missing",
		Retryable:  false,
		Message:    "TECHSTACK_ENCRYPTION_KEY is missing or not a 32-byte key; nothing was read, decrypted or written",
		UserGuidance: []string{
			"Provide the installation's TECHSTACK_ENCRYPTION_KEY for this run; execute reads the legacy values with it and writes them to the Wallet.",
		},
	}
}

// ExecuteIsASubcommand refuses the old plan --execute flag and points to the
// subcommand.
func ExecuteIsASubcommand() *Denial {
	return &Denial{
		ErrorCode:  ErrorCodeExecuteIsASubcommand,
		ReasonCode: "use_execute_subcommand",
		Retryable:  false,
		Message:    "plan only plans; nothing was read, decrypted or written",
		UserGuidance: []string{
			"Run `techstack wallet-migration execute --tenant <id>` to move the rows into the kombify Wallet.",
		},
	}
}
