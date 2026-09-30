#if CLOUD_DESKTOP
using System.Globalization;
using System.Resources;

namespace Kombify.TechStack.Client;

internal static class CloudLoginText
{
    private static readonly ResourceManager Resources = new(
        "Kombify.TechStack.Client.CloudLoginMessages", typeof(CloudLoginText).Assembly);

    internal static string Get(string key) => Resources.GetString(key, CultureInfo.CurrentUICulture)
        ?? throw new InvalidOperationException("Missing Cloud login message: " + key);
}
#endif
