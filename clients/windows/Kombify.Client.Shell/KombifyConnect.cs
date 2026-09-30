using System.Net;
using System.Net.Http.Headers;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using System.Text.Json.Serialization;
using Microsoft.Win32;

namespace Kombify.Client.Shell;

// kombify Connect installation enrollment for native Windows clients
// (kombify-Core/standards/CONNECT-CONTRACT-STANDARD.md sections 2, 4 and 5).
// The client proves possession of a non-exportable P-256 key with DPoP on
// every Connect call; the key never leaves the Windows key storage provider.

/// <summary>Declared custody of the installation key (contract section 2, NATIVE-CLIENT-PLATFORM-STANDARD 5.1).</summary>
public enum ConnectCustodyClass
{
    Hardware,
    OsProtected,
}

/// <summary>
/// The installation's DPoP key: a persisted, non-exportable CNG key. The TPM-backed
/// Microsoft Platform Crypto Provider is tried first (hardware custody); without a
/// usable TPM the per-user Microsoft Software Key Storage Provider holds it, which
/// DPAPI protects at rest (os-protected custody). The class never upgrades a claim.
/// </summary>
public sealed class ConnectDeviceKey : IDisposable
{
    private static readonly CngProvider PlatformProvider = new("Microsoft Platform Crypto Provider");

    private readonly ECDsaCng _ecdsa;

    private ConnectDeviceKey(CngKey key, ConnectCustodyClass custody)
    {
        _ecdsa = new ECDsaCng(key);
        Custody = custody;
        var parameters = _ecdsa.ExportParameters(includePrivateParameters: false);
        X = Base64Url(parameters.Q.X!);
        Y = Base64Url(parameters.Q.Y!);
        // RFC 7638: members in lexicographic order, no whitespace.
        Thumbprint = Base64Url(SHA256.HashData(Encoding.UTF8.GetBytes(
            $"{{\"crv\":\"P-256\",\"kty\":\"EC\",\"x\":\"{X}\",\"y\":\"{Y}\"}}")));
    }

    public ConnectCustodyClass Custody { get; }
    public string X { get; }
    public string Y { get; }
    /// <summary>RFC 7638 JWK thumbprint, the Connect <c>dpop_jkt</c>.</summary>
    public string Thumbprint { get; }

    public static ConnectDeviceKey OpenOrCreate(string keyName)
    {
        if (!OperatingSystem.IsWindows())
        {
            throw new PlatformNotSupportedException("Windows CNG is required for the Connect installation key.");
        }

        foreach (var (provider, custody) in new[]
                 {
                     (PlatformProvider, ConnectCustodyClass.Hardware),
                     (CngProvider.MicrosoftSoftwareKeyStorageProvider, ConnectCustodyClass.OsProtected),
                 })
        {
            try
            {
                var key = CngKey.Exists(keyName, provider)
                    ? CngKey.Open(keyName, provider)
                    : CngKey.Create(CngAlgorithm.ECDsaP256, keyName, new CngKeyCreationParameters
                    {
                        Provider = provider,
                        ExportPolicy = CngExportPolicies.None,
                        KeyCreationOptions = CngKeyCreationOptions.None,
                    });
                return new ConnectDeviceKey(key, custody);
            }
            catch (CryptographicException) when (provider == PlatformProvider)
            {
                // No TPM 2.0 or the platform provider refused: fall back to os-protected custody.
            }
        }

        throw new CryptographicException("No Windows key storage provider could hold the Connect installation key.");
    }

    /// <summary>RFC 9449 DPoP proof for one request (ES256, <c>typ dpop+jwt</c>).</summary>
    public string CreateProof(string method, Uri url)
    {
        var header = JsonSerializer.Serialize(new
        {
            alg = "ES256",
            typ = "dpop+jwt",
            jwk = new { kty = "EC", crv = "P-256", x = X, y = Y },
        });
        var htu = url.GetLeftPart(UriPartial.Path);
        var payload = JsonSerializer.Serialize(new
        {
            htm = method.ToUpperInvariant(),
            htu,
            iat = DateTimeOffset.UtcNow.ToUnixTimeSeconds(),
            jti = Guid.NewGuid().ToString("N"),
        });
        var signingInput = $"{Base64Url(Encoding.UTF8.GetBytes(header))}.{Base64Url(Encoding.UTF8.GetBytes(payload))}";
        // .NET emits the IEEE P1363 r||s form that JWS ES256 requires.
        var signature = _ecdsa.SignData(Encoding.ASCII.GetBytes(signingInput), HashAlgorithmName.SHA256);
        return $"{signingInput}.{Base64Url(signature)}";
    }

    /// <summary>Removes the installation key from both providers (reset or revocation cleanup).</summary>
    public static void Delete(string keyName)
    {
        foreach (var provider in new[] { PlatformProvider, CngProvider.MicrosoftSoftwareKeyStorageProvider })
        {
            try
            {
                if (CngKey.Exists(keyName, provider))
                {
                    using var key = CngKey.Open(keyName, provider);
                    key.Delete();
                }
            }
            catch (CryptographicException)
            {
                // Provider unavailable on this machine.
            }
        }
    }

    /// <summary>Verifies a proof this key produced; used by the contract tests.</summary>
    public bool VerifyProof(string proof)
    {
        var lastDot = proof.LastIndexOf('.');
        var signature = Convert.FromBase64String(Pad(proof[(lastDot + 1)..].Replace('-', '+').Replace('_', '/')));
        return _ecdsa.VerifyData(Encoding.ASCII.GetBytes(proof[..lastDot]), signature, HashAlgorithmName.SHA256);
    }

    private static string Pad(string value) => value.PadRight(value.Length + (4 - value.Length % 4) % 4, '=');

    public void Dispose() => _ecdsa.Dispose();

    internal static string Base64Url(ReadOnlySpan<byte> bytes) =>
        Convert.ToBase64String(bytes).TrimEnd('=').Replace('+', '-').Replace('/', '_');
}

/// <summary>
/// The display-only <c>device_ref</c> (contract section 2): HMAC-SHA256 of the subject keyed
/// with the Windows MachineGuid. Every kombify app of one subject on this machine derives the
/// same value; the MachineGuid itself never leaves the machine.
/// </summary>
public static class ConnectDeviceRef
{
    public static string? Compute(string subjectId)
    {
        if (!OperatingSystem.IsWindows() || string.IsNullOrWhiteSpace(subjectId))
        {
            return null;
        }

        using var cryptography = RegistryKey.OpenBaseKey(RegistryHive.LocalMachine, RegistryView.Registry64)
            .OpenSubKey(@"SOFTWARE\Microsoft\Cryptography");
        if (cryptography?.GetValue("MachineGuid") is not string machineGuid || string.IsNullOrWhiteSpace(machineGuid))
        {
            return null;
        }

        var mac = HMACSHA256.HashData(
            Encoding.UTF8.GetBytes(machineGuid.Trim()),
            Encoding.UTF8.GetBytes($"kombify-connect-device:v1:{subjectId}"));
        return $"dev_{ConnectDeviceKey.Base64Url(mac)}";
    }
}

public sealed record ConnectInstallation
{
    [JsonPropertyName("installation_id")] public string InstallationId { get; init; } = "";
    [JsonPropertyName("subject_id")] public string SubjectId { get; init; } = "";
    [JsonPropertyName("app_id")] public string AppId { get; init; } = "";
    [JsonPropertyName("status")] public string Status { get; init; } = "";
    [JsonPropertyName("custody_class")] public string CustodyClass { get; init; } = "";
    [JsonPropertyName("device_ref")] public string? DeviceRef { get; init; }
    [JsonPropertyName("display_name")] public string DisplayName { get; init; } = "";
    [JsonPropertyName("platform")] public string Platform { get; init; } = "";
}

/// <summary>Structured Connect refusal (ConnectDenialV1); carries guidance, never secrets.</summary>
public sealed class ConnectDenialException(HttpStatusCode status, string reasonCode, string title, string body)
    : Exception($"{title}: {body} ({reasonCode})")
{
    public HttpStatusCode Status { get; } = status;
    public string ReasonCode { get; } = reasonCode;
    public string Title { get; } = title;
    public string Body { get; } = body;
}

/// <summary>Registers this app as a Connect installation (contract section 5).</summary>
public sealed class ConnectEnrollmentClient(HttpClient http, Uri connectOrigin)
{
    public async Task<ConnectInstallation> RegisterAsync(
        string accessToken,
        ConnectDeviceKey key,
        string appId,
        string displayName,
        string appVersion,
        string? deviceRef,
        CancellationToken cancellationToken)
    {
        var challenge = await SendAsync<ChallengeResponse>(
            HttpMethod.Post, "/v1/installations/challenges", accessToken, key, body: null, cancellationToken);
        var registration = new Dictionary<string, string>
        {
            ["challenge"] = challenge.Challenge,
            ["app_id"] = appId,
            ["platform"] = "windows",
            ["custody_class"] = key.Custody == ConnectCustodyClass.Hardware ? "hardware" : "os-protected",
            ["display_name"] = displayName.Length > 80 ? displayName[..80] : displayName,
            ["app_version"] = appVersion,
        };
        if (deviceRef is not null)
        {
            registration["device_ref"] = deviceRef;
        }

        return await SendAsync<ConnectInstallation>(
            HttpMethod.Post, "/v1/installations", accessToken, key, registration, cancellationToken);
    }

    /// <summary>The account's installations (contract section 5, step 3); needs a registered key.</summary>
    public async Task<IReadOnlyList<ConnectInstallation>> ListInstallationsAsync(
        string accessToken, ConnectDeviceKey key, CancellationToken cancellationToken)
    {
        var list = await SendAsync<InstallationList>(HttpMethod.Get, "/v1/installations", accessToken, key, null, cancellationToken);
        return list.Installations;
    }

    /// <summary>
    /// Confirms a pending installation of the same account. Gateway allows it only when this
    /// installation is active (contract section 5), so a fresh install cannot confirm itself.
    /// </summary>
    public Task<ConnectInstallation> ConfirmAsync(
        string accessToken, ConnectDeviceKey key, string installationId, CancellationToken cancellationToken) =>
        SendAsync<ConnectInstallation>(
            HttpMethod.Post, $"/v1/installations/{Uri.EscapeDataString(installationId)}/confirm", accessToken, key, null,
            cancellationToken);

    private async Task<T> SendAsync<T>(
        HttpMethod method, string path, string accessToken, ConnectDeviceKey key, object? body, CancellationToken cancellationToken)
    {
        var url = new Uri(connectOrigin, path);
        using var request = new HttpRequestMessage(method, url);
        request.Headers.Authorization = new AuthenticationHeaderValue("Bearer", accessToken);
        request.Headers.Add("DPoP", key.CreateProof(method.Method, url));
        if (body is not null)
        {
            request.Content = new StringContent(JsonSerializer.Serialize(body), Encoding.UTF8, "application/json");
        }

        using var response = await http.SendAsync(request, cancellationToken);
        var text = await response.Content.ReadAsStringAsync(cancellationToken);
        if (!response.IsSuccessStatusCode)
        {
            throw Denial(response.StatusCode, text);
        }

        return JsonSerializer.Deserialize<T>(text)
            ?? throw new InvalidOperationException("kombify Connect returned an empty response.");
    }

    private static ConnectDenialException Denial(HttpStatusCode status, string text)
    {
        try
        {
            using var document = JsonDocument.Parse(text);
            var root = document.RootElement;
            var guidance = root.GetProperty("user_guidance");
            return new ConnectDenialException(
                status,
                root.GetProperty("reason_code").GetString() ?? "connect_unknown",
                guidance.GetProperty("title").GetString() ?? "Connect request refused",
                guidance.GetProperty("body").GetString() ?? "");
        }
        catch (Exception ex) when (ex is JsonException or KeyNotFoundException or InvalidOperationException)
        {
            return new ConnectDenialException(status, "connect_unknown", "Connect request refused",
                $"kombify Connect answered HTTP {(int)status}.");
        }
    }

    private sealed record InstallationList
    {
        [JsonPropertyName("installations")] public List<ConnectInstallation> Installations { get; init; } = [];
    }

    private sealed record ChallengeResponse
    {
        [JsonPropertyName("challenge")] public string Challenge { get; init; } = "";
    }
}

/// <summary>Result of the Auth0 native login: the access token stays in memory only.</summary>
public sealed record Auth0NativeSession(string AccessToken, string? RefreshToken, string SubjectId);

/// <summary>
/// Auth0 authorization code + PKCE with a loopback redirect (RFC 8252). Login, signup and
/// account choice stay in Auth0's Universal Login in the system browser.
/// </summary>
public sealed class Auth0NativeLogin(HttpClient http, Uri issuer, string clientId, string audience, IReadOnlyList<int> loopbackPorts)
{
    public async Task<Auth0NativeSession> SignInAsync(Action<Uri> openBrowser, CancellationToken cancellationToken)
    {
        var verifier = ConnectDeviceKey.Base64Url(RandomNumberGenerator.GetBytes(32));
        var codeChallenge = ConnectDeviceKey.Base64Url(SHA256.HashData(Encoding.ASCII.GetBytes(verifier)));
        var state = ConnectDeviceKey.Base64Url(RandomNumberGenerator.GetBytes(16));
        using var listener = StartListener(out var redirectUri);

        var authorize = new UriBuilder(new Uri(issuer, "/authorize"))
        {
            Query = string.Join("&", new Dictionary<string, string>
            {
                ["response_type"] = "code",
                ["client_id"] = clientId,
                ["redirect_uri"] = redirectUri,
                ["audience"] = audience,
                ["scope"] = "openid profile offline_access",
                ["code_challenge"] = codeChallenge,
                ["code_challenge_method"] = "S256",
                ["state"] = state,
            }.Select(pair => $"{pair.Key}={Uri.EscapeDataString(pair.Value)}")),
        }.Uri;
        openBrowser(authorize);

        var context = await listener.GetContextAsync().WaitAsync(TimeSpan.FromMinutes(10), cancellationToken);
        var query = context.Request.QueryString;
        var ok = query["state"] == state && !string.IsNullOrEmpty(query["code"]);
        var page = Encoding.UTF8.GetBytes(ok
            ? "<html><body>kombify sign-in finished. You can close this tab.</body></html>"
            : "<html><body>kombify sign-in failed. Return to the app and try again.</body></html>");
        context.Response.ContentType = "text/html; charset=utf-8";
        await context.Response.OutputStream.WriteAsync(page, cancellationToken);
        context.Response.Close();
        if (!ok)
        {
            throw new InvalidOperationException(query["error_description"] ?? "Auth0 sign-in did not return a valid code.");
        }

        using var exchange = await http.PostAsync(new Uri(issuer, "/oauth/token"), new FormUrlEncodedContent(new Dictionary<string, string>
        {
            ["grant_type"] = "authorization_code",
            ["client_id"] = clientId,
            ["code"] = query["code"]!,
            ["code_verifier"] = verifier,
            ["redirect_uri"] = redirectUri,
        }), cancellationToken);
        var text = await exchange.Content.ReadAsStringAsync(cancellationToken);
        if (!exchange.IsSuccessStatusCode)
        {
            throw new InvalidOperationException($"Auth0 token exchange failed with HTTP {(int)exchange.StatusCode}.");
        }

        using var document = JsonDocument.Parse(text);
        var accessToken = document.RootElement.GetProperty("access_token").GetString()!;
        var refreshToken = document.RootElement.TryGetProperty("refresh_token", out var refresh) ? refresh.GetString() : null;
        return new Auth0NativeSession(accessToken, refreshToken, SubjectOf(accessToken));
    }

    /// <summary>Exchanges a stored refresh token; Auth0 rotation may return a new one.</summary>
    public async Task<Auth0NativeSession> RefreshAsync(string refreshToken, CancellationToken cancellationToken)
    {
        using var exchange = await http.PostAsync(new Uri(issuer, "/oauth/token"), new FormUrlEncodedContent(new Dictionary<string, string>
        {
            ["grant_type"] = "refresh_token",
            ["client_id"] = clientId,
            ["refresh_token"] = refreshToken,
        }), cancellationToken);
        var text = await exchange.Content.ReadAsStringAsync(cancellationToken);
        if (!exchange.IsSuccessStatusCode)
        {
            throw new InvalidOperationException($"Auth0 refresh failed with HTTP {(int)exchange.StatusCode}; sign in again.");
        }

        using var document = JsonDocument.Parse(text);
        var accessToken = document.RootElement.GetProperty("access_token").GetString()!;
        var rotated = document.RootElement.TryGetProperty("refresh_token", out var refresh) ? refresh.GetString() : null;
        return new Auth0NativeSession(accessToken, rotated ?? refreshToken, SubjectOf(accessToken));
    }

    private HttpListener StartListener(out string redirectUri)
    {
        foreach (var port in loopbackPorts)
        {
            var listener = new HttpListener();
            var prefix = $"http://127.0.0.1:{port}/oauth/callback/";
            listener.Prefixes.Add(prefix);
            try
            {
                listener.Start();
                redirectUri = prefix.TrimEnd('/');
                return listener;
            }
            catch (HttpListenerException)
            {
                listener.Close();
            }
        }

        throw new InvalidOperationException("No loopback port for the kombify sign-in callback is free.");
    }

    /// <summary>Reads <c>sub</c> for the device_ref only; the Gateway validates the token itself.</summary>
    private static string SubjectOf(string jwt)
    {
        var parts = jwt.Split('.');
        if (parts.Length < 2)
        {
            return "";
        }

        var payload = parts[1].Replace('-', '+').Replace('_', '/');
        payload = payload.PadRight(payload.Length + (4 - payload.Length % 4) % 4, '=');
        using var document = JsonDocument.Parse(Convert.FromBase64String(payload));
        return document.RootElement.TryGetProperty("sub", out var sub) ? sub.GetString() ?? "" : "";
    }
}
