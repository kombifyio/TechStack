using System.Net;
using System.Net.Sockets;
using System.Text.Json;
using System.Text.Json.Serialization;
using System.Text.RegularExpressions;

namespace Kombify.TechStack.Client;

internal sealed record ClientConnectionProfile
{
    [JsonPropertyName("version")]
    public string Version { get; init; } = "";

    [JsonPropertyName("deployment_mode")]
    public string DeploymentMode { get; init; } = "";

    [JsonPropertyName("base_url")]
    public string BaseUrl { get; init; } = "";

    [JsonPropertyName("instance_id")]
    public string InstanceId { get; init; } = "";

    [JsonPropertyName("workspace_id")]
    [JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    public string? WorkspaceId { get; init; }

    [JsonPropertyName("oidc")]
    public ClientOidcProfile? Oidc { get; init; }

    [JsonPropertyName("capabilities")]
    public List<string>? Capabilities { get; init; }

    [JsonPropertyName("api_versions")]
    public Dictionary<string, string>? ApiVersions { get; init; }

    [JsonPropertyName("sync")]
    public ClientSyncProfile? Sync { get; init; }
}

internal sealed record ClientOidcProfile
{
    [JsonPropertyName("issuer")]
    public string Issuer { get; init; } = "";

    [JsonPropertyName("client_id")]
    public string ClientId { get; init; } = "";

    [JsonPropertyName("audience")]
    [JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    public string? Audience { get; init; }

    [JsonPropertyName("scopes")]
    public List<string>? Scopes { get; init; }

    [JsonPropertyName("flow")]
    public string Flow { get; init; } = "";
}

internal sealed record ClientSyncProfile
{
    [JsonPropertyName("offline_read")]
    public bool? OfflineRead { get; init; }

    [JsonPropertyName("offline_write")]
    public string OfflineWrite { get; init; } = "";

    [JsonPropertyName("etag")]
    public bool? Etag { get; init; }

    [JsonPropertyName("tombstones")]
    public bool? Tombstones { get; init; }
}

internal sealed class ClientProfileException(string message) : Exception(message);

internal static partial class ClientConnectionProfileValidator
{
    private static readonly JsonSerializerOptions StrictJson = new(JsonSerializerDefaults.Web)
    {
        UnmappedMemberHandling = JsonUnmappedMemberHandling.Disallow,
    };

    private static readonly JsonSerializerOptions PersistedJson = new(StrictJson)
    {
        WriteIndented = true,
    };

    private static readonly HashSet<string> DeploymentModes = ["cloud", "self_hosted", "local"];
    private static readonly HashSet<string> OidcFlows =
        ["authorization_code_pkce", "device_authorization", "local_bootstrap"];

    [GeneratedRegex("^[a-z0-9][a-z0-9._-]{2,127}$", RegexOptions.CultureInvariant)]
    private static partial Regex InstanceIdPattern();

    [GeneratedRegex("^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$", RegexOptions.CultureInvariant)]
    private static partial Regex WorkspaceIdPattern();

    [GeneratedRegex("^[a-z][a-z0-9.-]*$", RegexOptions.CultureInvariant)]
    private static partial Regex CapabilityPattern();

    [GeneratedRegex("^[A-Za-z0-9:._/-]+$", RegexOptions.CultureInvariant)]
    private static partial Regex ScopePattern();

    [GeneratedRegex("^v?[0-9]+(?:\\.[0-9]+){0,2}(?:[-+][0-9A-Za-z.-]+)?$", RegexOptions.CultureInvariant)]
    private static partial Regex ApiVersionPattern();

    public static Uri NormalizeConfiguredEndpoint(string value)
    {
        if (!Uri.TryCreate(value, UriKind.Absolute, out var endpoint)
            || (endpoint.Scheme != Uri.UriSchemeHttps && endpoint.Scheme != Uri.UriSchemeHttp)
            || !string.IsNullOrEmpty(endpoint.UserInfo)
            || !string.IsNullOrEmpty(endpoint.Query)
            || !string.IsNullOrEmpty(endpoint.Fragment)
            || (endpoint.AbsolutePath != "/" && endpoint.AbsolutePath != ""))
        {
            throw new ClientProfileException("The server address must be an HTTPS origin without credentials, query, fragment, or path.");
        }

        if (endpoint.Scheme == Uri.UriSchemeHttp && !IsLoopback(endpoint))
        {
            throw new ClientProfileException("Remote Techstack instances require HTTPS; HTTP is allowed only on loopback.");
        }

        return new UriBuilder(endpoint.Scheme, endpoint.Host, endpoint.Port).Uri;
    }

    public static ClientConnectionProfile ParseAndValidate(string json, Uri configuredEndpoint)
    {
        ClientConnectionProfile profile;
        try
        {
            profile = JsonSerializer.Deserialize<ClientConnectionProfile>(json, StrictJson)
                ?? throw new ClientProfileException("The discovery response is empty.");
        }
        catch (JsonException error)
        {
            throw new ClientProfileException($"The discovery response is not a strict ClientConnectionProfile v1 document: {error.Message}");
        }

        if (profile.Version != "1")
        {
            throw new ClientProfileException("The Techstack instance does not advertise ClientConnectionProfile version 1.");
        }
        if (!DeploymentModes.Contains(profile.DeploymentMode))
        {
            throw new ClientProfileException("The discovery deployment_mode is unsupported.");
        }
        if (!InstanceIdPattern().IsMatch(profile.InstanceId))
        {
            throw new ClientProfileException("The discovery instance_id is invalid.");
        }
        if (profile.WorkspaceId is not null && !WorkspaceIdPattern().IsMatch(profile.WorkspaceId))
        {
            throw new ClientProfileException("The discovery workspace_id is invalid.");
        }

        var profileEndpoint = ParseProfileEndpoint(profile.BaseUrl, profile.DeploymentMode, "base_url");
        if (!SameOrigin(configuredEndpoint, profileEndpoint))
        {
            throw new ClientProfileException("The discovery base_url does not match the server origin selected by the user.");
        }

        ValidateOidc(profile);
        ValidateCapabilities(profile.Capabilities);
        ValidateApiVersions(profile.ApiVersions);
        ValidateSync(profile.Sync);
        return profile;
    }

    public static void PersistPublicProfile(ClientConnectionProfile profile, string path)
    {
        var directory = Path.GetDirectoryName(path)
            ?? throw new ClientProfileException("The connection-profile path has no parent directory.");
        Directory.CreateDirectory(directory);
        var temporaryPath = path + ".tmp";
        File.WriteAllText(temporaryPath, JsonSerializer.Serialize(profile, PersistedJson));
        File.Move(temporaryPath, path, true);
    }

    private static void ValidateOidc(ClientConnectionProfile profile)
    {
        var oidc = profile.Oidc ?? throw new ClientProfileException("The discovery oidc configuration is required.");
        _ = ParseProfileEndpoint(oidc.Issuer, profile.DeploymentMode, "oidc.issuer", allowPath: true);
        if (string.IsNullOrWhiteSpace(oidc.ClientId) || oidc.ClientId.Length > 256)
        {
            throw new ClientProfileException("The discovery oidc.client_id is invalid.");
        }
        if (oidc.Audience is { Length: > 512 } || oidc.Audience is "")
        {
            throw new ClientProfileException("The discovery oidc.audience is invalid.");
        }
        if (oidc.Scopes is null || oidc.Scopes.Count == 0 || oidc.Scopes.Count != oidc.Scopes.Distinct().Count()
            || oidc.Scopes.Any(scope => !ScopePattern().IsMatch(scope)))
        {
            throw new ClientProfileException("The discovery oidc.scopes are invalid.");
        }
        if (!OidcFlows.Contains(oidc.Flow)
            || (profile.DeploymentMode == "cloud" && oidc.Flow != "authorization_code_pkce")
            || (profile.DeploymentMode == "self_hosted"
                && oidc.Flow is not ("authorization_code_pkce" or "device_authorization"))
            || (profile.DeploymentMode != "local" && oidc.Flow == "local_bootstrap"))
        {
            throw new ClientProfileException("The advertised native authentication flow is not valid for this deployment mode.");
        }
    }

    private static void ValidateCapabilities(List<string>? values)
    {
        if (values is null || values.Count != values.Distinct().Count()
            || values.Any(value => !CapabilityPattern().IsMatch(value)))
        {
            throw new ClientProfileException("The discovery capabilities are invalid.");
        }
    }

    private static void ValidateApiVersions(Dictionary<string, string>? values)
    {
        if (values is null || values.Count == 0
            || values.Any(entry => !CapabilityPattern().IsMatch(entry.Key) || !ApiVersionPattern().IsMatch(entry.Value)))
        {
            throw new ClientProfileException("The discovery api_versions are invalid.");
        }
    }

    private static void ValidateSync(ClientSyncProfile? sync)
    {
        if (sync is null || sync.OfflineRead is null || sync.Etag is null || sync.Tombstones is null
            || sync.OfflineWrite is not ("disabled" or "outbox"))
        {
            throw new ClientProfileException("The discovery sync contract is incomplete or invalid.");
        }
    }

    private static Uri ParseProfileEndpoint(string value, string mode, string field, bool allowPath = false)
    {
        if (!Uri.TryCreate(value, UriKind.Absolute, out var endpoint)
            || (endpoint.Scheme != Uri.UriSchemeHttps && endpoint.Scheme != Uri.UriSchemeHttp)
            || !string.IsNullOrEmpty(endpoint.UserInfo)
            || !string.IsNullOrEmpty(endpoint.Query)
            || !string.IsNullOrEmpty(endpoint.Fragment)
            || (!allowPath && endpoint.AbsolutePath != "/" && endpoint.AbsolutePath != ""))
        {
            throw new ClientProfileException($"The discovery {field} is not a valid public endpoint.");
        }
        if (endpoint.Scheme == Uri.UriSchemeHttp && (mode != "local" || !IsLoopback(endpoint)))
        {
            throw new ClientProfileException($"The discovery {field} requires HTTPS outside local loopback mode.");
        }
        return endpoint;
    }

    private static bool SameOrigin(Uri left, Uri right)
    {
        return left.Scheme.Equals(right.Scheme, StringComparison.OrdinalIgnoreCase)
            && left.IdnHost.Equals(right.IdnHost, StringComparison.OrdinalIgnoreCase)
            && left.Port == right.Port;
    }

    private static bool IsLoopback(Uri uri)
    {
        if (uri.Host.Equals("localhost", StringComparison.OrdinalIgnoreCase))
        {
            return true;
        }
        return IPAddress.TryParse(uri.Host, out var address)
            && (IPAddress.IsLoopback(address)
                || address.AddressFamily == AddressFamily.InterNetworkV6 && address.Equals(IPAddress.IPv6Loopback));
    }
}
