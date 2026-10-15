using System.Resources;

namespace Kombify.TechStack.Client;

internal static class LocalRuntimeMessages
{
    internal static string WalletRevealTitle => Resources.GetString(nameof(WalletRevealTitle)) ?? string.Empty;
    internal static string WalletRevealPrompt => Resources.GetString(nameof(WalletRevealPrompt)) ?? string.Empty;
    internal static string WalletRevealFailed => Resources.GetString(nameof(WalletRevealFailed)) ?? string.Empty;
    private static readonly ResourceManager Resources = new(
        "Kombify.TechStack.Client.LocalRuntimeMessages", typeof(LocalRuntimeMessages).Assembly);

    internal static string IdentityVerificationFailed =>
        Resources.GetString(nameof(IdentityVerificationFailed)) ?? string.Empty;

    internal static string RecoveryIncomplete => Resources.GetString(nameof(RecoveryIncomplete)) ?? string.Empty;
    internal static string RecoveryDatabaseRunning => Resources.GetString(nameof(RecoveryDatabaseRunning)) ?? string.Empty;
    internal static string RecoverySnapshotMissing => Resources.GetString(nameof(RecoverySnapshotMissing)) ?? string.Empty;
    internal static string RecoveryRestoreFailed => Resources.GetString(nameof(RecoveryRestoreFailed)) ?? string.Empty;
    internal static string RecoveryInstallationPending => Resources.GetString(nameof(RecoveryInstallationPending)) ?? string.Empty;
}
