namespace Kombify.TechStack.Client;

// Product authority is a property of the installed edition, not user settings.
internal static class ClientAuthority
{
    internal const string CloudOrigin = "https://techstack.kombify.io/";

    internal static HttpClient CreateHttpClient() =>
        new(new HttpClientHandler { AllowAutoRedirect = false }) { Timeout = TimeSpan.FromSeconds(15) };

    internal static bool SameOrigin(Uri destination, Uri authority) =>
        destination.Scheme == authority.Scheme && destination.Host == authority.Host
        && destination.Port == authority.Port && string.IsNullOrEmpty(destination.UserInfo);

    internal static void RequireOrigin(string value, params string[] origins)
    {
        if (!Uri.TryCreate(value, UriKind.Absolute, out var uri)
            || !origins.Any(origin => SameOrigin(uri, new Uri(origin))))
            throw new InvalidOperationException("The configured endpoint does not belong to this installer's product authority.");
    }

    internal static void Validate(ClientConfig config)
    {
#if CLOUD_DESKTOP
        RequireOrigin(config.CloudUiUrl, CloudOrigin);
        RequireOrigin(config.CloudUrl, "https://kombify.io/", "https://app.kombify.io/");
        RequireOrigin(config.CloudDeviceUrl, "https://kombify.io/", "https://app.kombify.io/");
        RequireOrigin(config.CloudDeviceCodeEndpoint, "https://app.kombify.io/");
        RequireOrigin(config.CloudDevicePollEndpoint, "https://app.kombify.io/");
        RequireEndpoint(config.CloudDeviceCodeEndpoint, "https://app.kombify.io/api/v1/tools/auth/device-code");
        RequireEndpoint(config.CloudDevicePollEndpoint, "https://app.kombify.io/api/v1/tools/auth/device-code/poll");
        RequireOrigin(config.ConnectOrigin, "https://connect.kombify.io/");
        RequireOrigin(config.ConnectAuth0Issuer, "https://login.kombify.io/");
        RequireOrigin(config.ConnectAuth0Audience, "https://api.kombify.io/");
#else
        if (config.Mode != "local") return;
        if (!Uri.TryCreate(config.LocalUiUrl, UriKind.Absolute, out var local)
            || local.Scheme != Uri.UriSchemeHttp || !local.IsLoopback
            || !string.IsNullOrEmpty(local.UserInfo) || local.Port is < 1 or >= 65535
            || local.AbsolutePath != "/" || local.Query.Length != 0 || local.Fragment.Length != 0)
            throw new InvalidOperationException("The bundled runtime requires a loopback HTTP origin.");
        RequireOrigin(config.LocalOnboardingUrl, config.LocalUiUrl);
#endif
    }

    private static void RequireEndpoint(string value, string expected)
    {
        if (!Uri.TryCreate(value, UriKind.Absolute, out var uri) || uri.AbsoluteUri != expected)
            throw new InvalidOperationException("The credential endpoint must use the installed Cloud authentication route.");
    }

    internal static bool AllowsProductNavigation(Uri destination, string selectedAuthority) =>
        Uri.TryCreate(DesktopEdition.IsLocal ? selectedAuthority : CloudOrigin, UriKind.Absolute, out var authority)
        && SameOrigin(destination, authority);
}
