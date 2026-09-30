using System.Resources;

namespace Kombify.TechStack.Client;

internal static class LocalRuntimeMessages
{
    private static readonly ResourceManager Resources = new(
        "Kombify.TechStack.Client.LocalRuntimeMessages", typeof(LocalRuntimeMessages).Assembly);

    internal static string IdentityVerificationFailed =>
        Resources.GetString(nameof(IdentityVerificationFailed)) ?? string.Empty;
}
