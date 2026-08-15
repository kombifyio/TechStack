using System.Diagnostics;
using System.Drawing;
using System.Net;
using System.Net.Sockets;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using System.Text.Json.Serialization;
using Microsoft.Web.WebView2.Core;
using Microsoft.Web.WebView2.WinForms;

namespace Kombify.TechStack.Client;

internal static class Program
{
    [STAThread]
    private static void Main(string[] args)
    {
        ApplicationConfiguration.Initialize();
        Application.Run(new ClientWindow(ClientConfig.Load(args)));
    }
}

internal sealed record ClientConfig
{
    private const string LegacyLocalOnboardingUrl = "http://127.0.0.1:5260/client/local?client=windows";
    private const string DefaultLocalOnboardingUrl = "http://127.0.0.1:5260/client/onboarding?client=windows";

    public string Mode { get; init; } = "local";
    public string CloudUrl { get; init; } = "https://kombify.io/device";
    public string CloudDeviceUrl { get; init; } = "https://kombify.io/device";
    public string CloudUiUrl { get; init; } = "https://techstack.kombify.io/login?manual=1&client=windows";
    public string CloudDeviceCodeEndpoint { get; init; } =
        "https://app.kombify.io/api/v1/tools/auth/device-code";
    public string CloudDevicePollEndpoint { get; init; } =
        "https://app.kombify.io/api/v1/tools/auth/device-code/poll";
    public string ToolName { get; init; } = "stack";
    public string LocalUiUrl { get; init; } = "http://127.0.0.1:5260/";
    public string LocalOnboardingUrl { get; init; } = DefaultLocalOnboardingUrl;
    public string ServerUrl { get; init; } = "";
    public string RuntimeExecutablePath { get; init; } = "";
    public string RuntimeDataDir { get; init; } = Path.Combine(StateDirectory(), "runtime");
    public int RuntimeStartupTimeoutSeconds { get; init; } = 120;
    public bool AutoStartRuntime { get; init; } = true;

    public static ClientConfig Load(string[] args)
    {
        var config = Read(ConfigPath()) ?? new ClientConfig();
        if (config.LocalOnboardingUrl.Equals(LegacyLocalOnboardingUrl, StringComparison.OrdinalIgnoreCase))
        {
            config = config with { LocalOnboardingUrl = DefaultLocalOnboardingUrl };
        }

        for (var i = 0; i < args.Length; i++)
        {
            if (args[i] == "--url" && i + 1 < args.Length)
            {
                config = config with { Mode = "server", ServerUrl = args[++i] };
                continue;
            }

            if (args[i] == "--mode" && i + 1 < args.Length)
            {
                config = config with { Mode = args[++i] };
                continue;
            }

            if (args[i] == "--cloud-ui-url" && i + 1 < args.Length)
            {
                config = config with { Mode = "cloud", CloudUiUrl = args[++i] };
                continue;
            }

            if (args[i] == "--local-ui-url" && i + 1 < args.Length)
            {
                config = config with { Mode = "local", LocalUiUrl = args[++i] };
                continue;
            }

            if (args[i] == "--local-onboarding-url" && i + 1 < args.Length)
            {
                config = config with { Mode = "local", LocalOnboardingUrl = args[++i] };
                continue;
            }

            if (args[i] == "--runtime-exe" && i + 1 < args.Length)
            {
                config = config with { RuntimeExecutablePath = args[++i] };
                continue;
            }

            if (args[i] == "--runtime-data-dir" && i + 1 < args.Length)
            {
                config = config with { RuntimeDataDir = args[++i] };
                continue;
            }

            if (args[i] == "--runtime-startup-timeout-seconds" && i + 1 < args.Length
                && int.TryParse(args[++i], out var timeoutSeconds))
            {
                config = config with { RuntimeStartupTimeoutSeconds = timeoutSeconds };
                continue;
            }

            if (args[i] == "--no-runtime-start")
            {
                config = config with { AutoStartRuntime = false };
            }
        }

        return config;
    }

    public string InitialUrl()
    {
        if (Mode.Equals("server", StringComparison.OrdinalIgnoreCase) && IsHttp(ServerUrl))
        {
            return ServerUrl;
        }

        if (Mode.Equals("cloud", StringComparison.OrdinalIgnoreCase) && IsHttp(CloudUiUrl))
        {
            return CloudUiUrl;
        }

        if (Mode.Equals("cloud", StringComparison.OrdinalIgnoreCase) && IsHttp(CloudUrl))
        {
            return CloudUrl;
        }

        return IsHttp(LocalOnboardingUrl) ? LocalOnboardingUrl : LocalUiUrl;
    }

    public Uri? LocalUiUri()
    {
        return IsHttp(LocalUiUrl) ? new Uri(LocalUiUrl) : null;
    }

    public string LocalOrigin()
    {
        var uri = LocalUiUri();
        if (uri is null)
        {
            return "http://127.0.0.1:5260";
        }

        return new UriBuilder(uri.Scheme, uri.Host, uri.Port).Uri.ToString().TrimEnd('/');
    }

    public string RuntimeLogPath()
    {
        return Path.Combine(RuntimeDataDir, "techstack-runtime.log");
    }

    public static string ConfigPath()
    {
        return Path.Combine(
            StateDirectory(),
            "client.json");
    }

    public static string StateDirectory()
    {
        var defaultPath = Path.Combine(
            Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData),
            "kombify",
            "techstack-client");
        var configured = Environment.GetEnvironmentVariable("TECHSTACK_CLIENT_STATE_DIR")?.Trim();
        if (string.IsNullOrWhiteSpace(configured) || !Path.IsPathRooted(configured))
        {
            return defaultPath;
        }

        var allowedRoot = Path.GetFullPath(Path.Combine(
            Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData),
            "kombify")).TrimEnd(Path.DirectorySeparatorChar, Path.AltDirectorySeparatorChar);
        var resolved = Path.GetFullPath(configured).TrimEnd(Path.DirectorySeparatorChar, Path.AltDirectorySeparatorChar);
        var allowedPrefix = allowedRoot + Path.DirectorySeparatorChar;
        return !resolved.Equals(allowedRoot, StringComparison.OrdinalIgnoreCase)
            && (resolved + Path.DirectorySeparatorChar).StartsWith(allowedPrefix, StringComparison.OrdinalIgnoreCase)
            ? resolved
            : defaultPath;
    }

    public static string CredentialTarget(string baseTarget)
    {
        var defaultPath = Path.GetFullPath(Path.Combine(
            Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData),
            "kombify",
            "techstack-client"))
            .TrimEnd(Path.DirectorySeparatorChar, Path.AltDirectorySeparatorChar);
        var statePath = Path.GetFullPath(StateDirectory())
            .TrimEnd(Path.DirectorySeparatorChar, Path.AltDirectorySeparatorChar);
        if (statePath.Equals(defaultPath, StringComparison.OrdinalIgnoreCase))
        {
            return baseTarget;
        }

        // Test/preview installations must never share Credential Manager names
        // with the canonical product state. Derive a stable, non-secret suffix
        // from the isolated state path so restart and reset use the same target.
        var normalized = statePath.ToUpperInvariant();
        var digest = SHA256.HashData(Encoding.UTF8.GetBytes(normalized));
        var suffix = Convert.ToHexString(digest).ToLowerInvariant()[..16];
        return $"{baseTarget}/state-{suffix}";
    }

    private static ClientConfig? Read(string path)
    {
        if (!File.Exists(path))
        {
            return null;
        }

        try
        {
            return JsonSerializer.Deserialize<ClientConfig>(
                File.ReadAllText(path),
                new JsonSerializerOptions { PropertyNameCaseInsensitive = true });
        }
        catch
        {
            return null;
        }
    }

    private static bool IsHttp(string value)
    {
        return Uri.TryCreate(value, UriKind.Absolute, out var uri)
            && (uri.Scheme == Uri.UriSchemeHttp || uri.Scheme == Uri.UriSchemeHttps);
    }
}

internal sealed record CloudDeviceCodeResponse
{
    [JsonPropertyName("device_code")]
    public string DeviceCode { get; init; } = "";

    [JsonPropertyName("user_code")]
    public string UserCode { get; init; } = "";

    [JsonPropertyName("verification_uri")]
    public string VerificationUri { get; init; } = "";

    [JsonPropertyName("verification_uri_complete")]
    public string VerificationUriComplete { get; init; } = "";

    [JsonPropertyName("expires_in")]
    public int ExpiresIn { get; init; } = 900;

    [JsonPropertyName("interval")]
    public int Interval { get; init; } = 5;
}

internal sealed record CloudToolTokenResponse
{
    [JsonPropertyName("access_token")]
    public string AccessToken { get; init; } = "";

    [JsonPropertyName("refresh_token")]
    public string RefreshToken { get; init; } = "";

    [JsonPropertyName("expires_at")]
    public string ExpiresAt { get; init; } = "";

    [JsonPropertyName("user_id")]
    public string UserId { get; init; } = "";
}

internal sealed record CloudEntitlementResponse
{
    [JsonPropertyName("tool")]
    public string Tool { get; init; } = "";

    [JsonPropertyName("expires_at")]
    public string? ExpiresAt { get; init; }
}

internal sealed record CloudAuthorizationResponse
{
    [JsonPropertyName("token")]
    public CloudToolTokenResponse Token { get; init; } = new();

    [JsonPropertyName("entitlements")]
    public CloudEntitlementResponse? Entitlements { get; init; }
}

internal sealed class ClientWindow : Form
{
    private static readonly JsonSerializerOptions JsonOptions = new(JsonSerializerDefaults.Web);
    private static readonly object RuntimeLogLock = new();
    private const string LocalDeviceTokenEnv = "TECHSTACK_LOCAL_DEVICE_TOKEN";
    private const string LocalDeviceTokenHeader = "X-TechStack-Device-Token";
    private const string LocalRuntimeSessionCredentialTarget = "kombify/techstack/local/runtime-session-secret";
    private const string LocalDeviceCredentialTarget = "kombify/techstack/local/device-session-token";

    private readonly ClientConfig _config;
    private readonly WebView2 _webView = new() { Dock = DockStyle.Fill };
    private readonly HttpClient _httpClient = new() { Timeout = TimeSpan.FromSeconds(15) };
    private CancellationTokenSource? _cloudLoginCancellation;
    private Process? _runtimeProcess;
    private ClientConnectionProfile? _boundServerProfile;

    public ClientWindow(ClientConfig config)
    {
        _config = config;
        Text = "kombify TechStack Client";
        MinimumSize = new Size(980, 680);
        StartPosition = FormStartPosition.CenterScreen;
        WindowState = FormWindowState.Maximized;
        BackColor = Color.FromArgb(247, 249, 252);

        var icon = LoadIcon();
        if (icon is not null)
        {
            Icon = icon;
        }

        Controls.Add(_webView);
        Shown += async (_, _) => await StartAsync();
        FormClosed += (_, _) =>
        {
            _cloudLoginCancellation?.Cancel();
            StopStartedLocalRuntime();
        };
    }

    private async Task StartAsync()
    {
        try
        {
            var userData = Path.Combine(ClientConfig.StateDirectory(), "webview2");
            Directory.CreateDirectory(userData);

            var env = await CoreWebView2Environment.CreateAsync(null, userData);
            await _webView.EnsureCoreWebView2Async(env);
            _webView.CoreWebView2.NavigationStarting += OnNavigationStarting;

            if (IsLocalMode())
            {
                NavigateHtml(RenderLocalRuntimeStarting(_config.LocalOrigin()));
                if (!await EnsureLocalRuntimeAsync())
                {
                    return;
                }

                await BootstrapLocalDeviceSessionAsync();
            }

            if (IsServerMode())
            {
                NavigateHtml(RenderServerBindingStarting(_config.ServerUrl));
                _boundServerProfile = await BindServerProfileAsync();
                if (_boundServerProfile is null)
                {
                    return;
                }
            }

            _webView.CoreWebView2.Navigate(_boundServerProfile?.BaseUrl ?? _config.InitialUrl());
        }
        catch (Exception ex)
        {
            Controls.Clear();
            Controls.Add(new Label
            {
                Dock = DockStyle.Fill,
                TextAlign = ContentAlignment.MiddleCenter,
                Text = $"kombify TechStack Client could not start WebView2.\n\n{ex.Message}",
            });
        }
    }

    private bool IsLocalMode()
    {
        return _config.Mode.Equals("local", StringComparison.OrdinalIgnoreCase);
    }

    private bool IsServerMode()
    {
        return _config.Mode.Equals("server", StringComparison.OrdinalIgnoreCase);
    }

    private async Task<ClientConnectionProfile?> BindServerProfileAsync()
    {
        try
        {
            var configuredEndpoint = ClientConnectionProfileValidator.NormalizeConfiguredEndpoint(_config.ServerUrl);
            var discoveryEndpoint = new Uri(configuredEndpoint, "/.well-known/kombify-client");
            using var request = new HttpRequestMessage(HttpMethod.Get, discoveryEndpoint);
            request.Headers.Accept.ParseAdd("application/json");
            using var response = await _httpClient.SendAsync(request, HttpCompletionOption.ResponseHeadersRead);
            if (!response.IsSuccessStatusCode)
            {
                ShowServerBindingFailure(
                    $"The Techstack instance returned {(int)response.StatusCode} {response.StatusCode} from client discovery.",
                    "Check the instance's public URL and native OIDC configuration, then retry.");
                return null;
            }
            if (!string.Equals(response.Content.Headers.ContentType?.MediaType, "application/json", StringComparison.OrdinalIgnoreCase))
            {
                throw new ClientProfileException("The discovery endpoint did not return application/json.");
            }
            if (response.Content.Headers.ContentLength is > 65536)
            {
                throw new ClientProfileException("The discovery response exceeds the 64 KiB client-profile limit.");
            }

            var body = await response.Content.ReadAsStringAsync();
            if (Encoding.UTF8.GetByteCount(body) > 65536)
            {
                throw new ClientProfileException("The discovery response exceeds the 64 KiB client-profile limit.");
            }
            var profile = ClientConnectionProfileValidator.ParseAndValidate(body, configuredEndpoint);
            var path = Path.Combine(ClientConfig.StateDirectory(), "connection-profile.json");
            ClientConnectionProfileValidator.PersistPublicProfile(profile, path);
            return profile;
        }
        catch (Exception error) when (error is ClientProfileException or HttpRequestException or TaskCanceledException)
        {
            ShowServerBindingFailure(
                error.Message,
                "Verify the HTTPS endpoint or use a fresh Techstack pairing link from the instance owner.");
            return null;
        }
    }

    private void ShowServerBindingFailure(string body, string nextStep)
    {
        NavigateHtml(RenderFallback(
            "Techstack connection could not be verified.",
            body,
            $"<p class=\"copy small\"><strong>Next step:</strong> {Html(nextStep)}</p>"));
    }

    private async Task<bool> EnsureLocalRuntimeAsync()
    {
        if (await LocalRuntimeReadyAsync(TimeSpan.FromSeconds(2)))
        {
            return true;
        }

        if (!_config.AutoStartRuntime)
        {
            NavigateHtml(RenderFallback(
                "Local TechStack runtime is not running.",
                $"Start TechStack locally, then open the client again. Expected local runtime at {_config.LocalOrigin()}."));
            return false;
        }

        var runtimeExe = ResolveRuntimeExecutablePath();
        if (string.IsNullOrWhiteSpace(runtimeExe))
        {
            NavigateHtml(RenderFallback(
                "Local TechStack runtime was not found.",
                "The Windows client package must include techstack.exe next to kombify-techstack-client.exe.",
                RuntimeLogExtraHtml(_config.RuntimeLogPath())));
            return false;
        }

        Directory.CreateDirectory(_config.RuntimeDataDir);
        var logPath = _config.RuntimeLogPath();
        Directory.CreateDirectory(Path.GetDirectoryName(logPath) ?? _config.RuntimeDataDir);
        AppendRuntimeLog(logPath, $"[{DateTimeOffset.Now:u}] starting local runtime: {runtimeExe}");

        try
        {
            var start = new ProcessStartInfo
            {
                FileName = runtimeExe,
                WorkingDirectory = Path.GetDirectoryName(runtimeExe) ?? AppContext.BaseDirectory,
                UseShellExecute = false,
                CreateNoWindow = true,
                RedirectStandardOutput = true,
                RedirectStandardError = true,
            };
            start.Environment["TECHSTACK_LISTEN_ADDR"] = RuntimeListenAddr();
            start.Environment["TECHSTACK_GRPC_ADDR"] = RuntimeGrpcAddr();
            start.Environment["TECHSTACK_DATA_DIR"] = _config.RuntimeDataDir;
            start.Environment["TECHSTACK_PB_DATA_DIR"] = Path.Combine(_config.RuntimeDataDir, "pb_data");
            start.Environment["TECHSTACK_RUNTIME_LOG_PATH"] = Path.Combine(_config.RuntimeDataDir, "runtime-logs.jsonl");
            start.Environment["TECHSTACK_PUBLIC_ORIGIN"] = _config.LocalOrigin();
            start.Environment["TECHSTACK_CORS_ORIGINS"] = _config.LocalOrigin();
            start.Environment["TECHSTACK_ENV"] = "local";
            start.Environment["KOMBIFY_EDITION"] = "selfhost-oss";
            start.Environment["TECHSTACK_EMBEDDED_POSTGRES"] = "1";
            start.Environment["TECHSTACK_EMBEDDED_POSTGRES_START_TIMEOUT_SECONDS"] = "120";
            start.Environment["TECHSTACK_V2_SESSION_SECRET"] = EnsureRuntimeSessionSecret();
            start.Environment["TECHSTACK_V2_SESSION_AUDIENCE"] = "techstack-local";
            start.Environment["TECHSTACK_V2_DEFAULT_TENANT_ID"] = "default";
			start.Environment[LocalDeviceTokenEnv] = EnsureLocalDeviceToken();
			var runtimeDirectory = Path.GetDirectoryName(runtimeExe) ?? AppContext.BaseDirectory;
			var stackKitsDirectory = Path.Combine(runtimeDirectory, "stackkits");
			if (Directory.Exists(stackKitsDirectory))
			{
				start.Environment["TECHSTACK_STACKKITS_DIR"] = stackKitsDirectory;
				var stackKitCLI = Path.Combine(stackKitsDirectory, "bin", "stackkit.exe");
				if (File.Exists(stackKitCLI))
				{
					start.Environment["TECHSTACK_STACKKIT_CLI"] = stackKitCLI;
				}
				var stackKitSpecTemplates = Path.Combine(stackKitsDirectory, "spec-templates");
				if (Directory.Exists(stackKitSpecTemplates))
				{
					start.Environment["TECHSTACK_STACKKIT_SPEC_TEMPLATES"] = stackKitSpecTemplates;
				}
			}
			start.Environment["TECHSTACK_AGENT_BINARY_LINUX_AMD64"] = Path.Combine(runtimeDirectory, "techstack-linux-amd64");
			start.Environment["TECHSTACK_STACKKIT_RELEASE_BUNDLE"] = Path.Combine(runtimeDirectory, "stackkit-release-linux-amd64.tar.gz");

            _runtimeProcess = new Process { StartInfo = start, EnableRaisingEvents = true };
            _runtimeProcess.OutputDataReceived += (_, e) => AppendRuntimeLog(logPath, e.Data);
            _runtimeProcess.ErrorDataReceived += (_, e) => AppendRuntimeLog(logPath, e.Data);
            _runtimeProcess.Start();
            _runtimeProcess.BeginOutputReadLine();
            _runtimeProcess.BeginErrorReadLine();
        }
        catch (Exception ex)
        {
            NavigateHtml(RenderFallback(
                "Local TechStack runtime could not start.",
                $"The Windows client could not start techstack.exe. {ex.Message}",
                RuntimeLogExtraHtml(logPath)));
            return false;
        }

        var timeout = TimeSpan.FromSeconds(Math.Clamp(_config.RuntimeStartupTimeoutSeconds, 3, 120));
        var deadline = DateTimeOffset.UtcNow.Add(timeout);

        while (DateTimeOffset.UtcNow < deadline)
        {
            await Task.Delay(TimeSpan.FromMilliseconds(500));

            if (_runtimeProcess is { HasExited: true })
            {
                NavigateHtml(RenderFallback(
                    "Local TechStack runtime stopped during startup.",
                    LocalRuntimeFailureDetail(logPath, _runtimeProcess.ExitCode),
                    RuntimeLogExtraHtml(logPath)));
                return false;
            }

            if (await LocalRuntimeReadyAsync(TimeSpan.FromSeconds(2)))
            {
                AppendRuntimeLog(logPath, $"[{DateTimeOffset.Now:u}] local runtime ready at {_config.LocalOrigin()}");
                return true;
            }
        }

        StopStartedLocalRuntime();
        NavigateHtml(RenderFallback(
            "Local TechStack runtime did not become ready.",
            $"The Windows client waited {timeout.TotalSeconds:N0} seconds for {_config.LocalOrigin()}.",
            RuntimeLogExtraHtml(logPath)));
        return false;
    }

    private async Task<bool> LocalRuntimeReadyAsync(TimeSpan timeout)
    {
        var uri = LocalAuthModeUri();
        if (uri is null)
        {
            return false;
        }

        using var timeoutCts = new CancellationTokenSource(timeout);
        try
        {
            using var request = new HttpRequestMessage(HttpMethod.Get, uri);
            using var response = await _httpClient.SendAsync(request, timeoutCts.Token);
            return response.IsSuccessStatusCode;
        }
        catch
        {
            return false;
        }
    }

    private Uri? LocalAuthModeUri()
    {
        var local = _config.LocalUiUri();
        return local is null ? null : new Uri(local, "/api/v1/auth/mode");
    }

    private Uri? LocalDeviceSessionUri()
    {
        var local = _config.LocalUiUri();
        return local is null ? null : new Uri(local, "/api/v1/auth/device-session");
    }

    private string RuntimeListenAddr()
    {
        var local = _config.LocalUiUri();
        return local is null ? "127.0.0.1:5260" : RuntimeAddr(local.Host, local.Port);
    }

    // The gRPC listener stays next to the HTTP listener: same host, port + 3
    // (default 5260 -> 5263), so a non-default local port remains conflict-free.
    private string RuntimeGrpcAddr()
    {
        var local = _config.LocalUiUri();
        return local is null ? "127.0.0.1:5263" : RuntimeAddr(local.Host, local.Port + 3);
    }

    private static string RuntimeAddr(string host, int port)
    {
        if (host.Equals("localhost", StringComparison.OrdinalIgnoreCase))
        {
            host = "127.0.0.1";
        }

        if (IPAddress.TryParse(host, out var ip) && ip.AddressFamily == AddressFamily.InterNetworkV6)
        {
            host = $"[{host}]";
        }

        return $"{host}:{port}";
    }

    private string ResolveRuntimeExecutablePath()
    {
        if (!string.IsNullOrWhiteSpace(_config.RuntimeExecutablePath) && File.Exists(_config.RuntimeExecutablePath))
        {
            return _config.RuntimeExecutablePath;
        }

        var bundled = Path.Combine(AppContext.BaseDirectory, "techstack.exe");
        return File.Exists(bundled) ? bundled : "";
    }

    private string EnsureRuntimeSessionSecret()
    {
        return EnsureLocalCredential(ClientConfig.CredentialTarget(LocalRuntimeSessionCredentialTarget));
    }

    private string EnsureLocalDeviceToken()
    {
        return EnsureLocalCredential(ClientConfig.CredentialTarget(LocalDeviceCredentialTarget));
    }

    private string ReadLocalDeviceToken()
    {
        return WindowsCredentialStore.Read(ClientConfig.CredentialTarget(LocalDeviceCredentialTarget))?.Trim() ?? "";
    }

    private static string EnsureLocalCredential(string target)
    {
        var existing = WindowsCredentialStore.Read(target)?.Trim();
        if (!string.IsNullOrWhiteSpace(existing) && existing.Length >= 64)
        {
            return existing;
        }

        var secret = Convert.ToHexString(RandomNumberGenerator.GetBytes(32)).ToLowerInvariant();
        WindowsCredentialStore.Write(target, Environment.UserName, secret);
        return secret;
    }

    private async Task BootstrapLocalDeviceSessionAsync()
    {
        var endpoint = LocalDeviceSessionUri();
        var token = ReadLocalDeviceToken();
        if (endpoint is null || string.IsNullOrWhiteSpace(token))
        {
            return;
        }

        try
        {
            using var request = new HttpRequestMessage(HttpMethod.Post, endpoint);
            request.Headers.TryAddWithoutValidation(LocalDeviceTokenHeader, token);
            using var response = await _httpClient.SendAsync(request);
            if (!response.IsSuccessStatusCode)
            {
                AppendRuntimeLog(
                    _config.RuntimeLogPath(),
                    $"[{DateTimeOffset.Now:u}] local device session skipped: {(int)response.StatusCode} {response.StatusCode}");
                return;
            }

            ImportSetCookieHeaders(endpoint, response);
            AppendRuntimeLog(_config.RuntimeLogPath(), $"[{DateTimeOffset.Now:u}] local device session restored");
        }
        catch (Exception ex)
        {
            AppendRuntimeLog(_config.RuntimeLogPath(), $"[{DateTimeOffset.Now:u}] local device session failed: {ex.Message}");
        }
    }

    private void ImportSetCookieHeaders(Uri endpoint, HttpResponseMessage response)
    {
        if (!response.Headers.TryGetValues("Set-Cookie", out var setCookieHeaders))
        {
            return;
        }

        foreach (var setCookie in setCookieHeaders)
        {
            ImportSetCookieHeader(endpoint, setCookie);
        }
    }

    private void ImportSetCookieHeader(Uri endpoint, string setCookie)
    {
        var parts = setCookie.Split(';', StringSplitOptions.RemoveEmptyEntries | StringSplitOptions.TrimEntries);
        if (parts.Length == 0)
        {
            return;
        }

        var equalsAt = parts[0].IndexOf('=');
        if (equalsAt <= 0)
        {
            return;
        }

        var name = parts[0][..equalsAt].Trim();
        var value = parts[0][(equalsAt + 1)..].Trim();
        var path = "/";
        var httpOnly = false;
        var secure = false;
        var sameSite = CoreWebView2CookieSameSiteKind.Lax;

        foreach (var attr in parts.Skip(1))
        {
            if (attr.Equals("HttpOnly", StringComparison.OrdinalIgnoreCase))
            {
                httpOnly = true;
                continue;
            }
            if (attr.Equals("Secure", StringComparison.OrdinalIgnoreCase))
            {
                secure = true;
                continue;
            }
            if (attr.StartsWith("Path=", StringComparison.OrdinalIgnoreCase))
            {
                path = attr["Path=".Length..].Trim();
                continue;
            }
            if (attr.Equals("SameSite=None", StringComparison.OrdinalIgnoreCase))
            {
                sameSite = CoreWebView2CookieSameSiteKind.None;
                continue;
            }
            if (attr.Equals("SameSite=Strict", StringComparison.OrdinalIgnoreCase))
            {
                sameSite = CoreWebView2CookieSameSiteKind.Strict;
            }
        }

        var cookie = _webView.CoreWebView2.CookieManager.CreateCookie(name, value, endpoint.Host, path);
        cookie.IsHttpOnly = httpOnly;
        cookie.IsSecure = secure;
        cookie.SameSite = sameSite;
        _webView.CoreWebView2.CookieManager.AddOrUpdateCookie(cookie);
    }

    private void StopStartedLocalRuntime()
    {
        var process = _runtimeProcess;
        if (process is null)
        {
            return;
        }

        try
        {
            if (!process.HasExited)
            {
                process.Kill(entireProcessTree: true);
                process.WaitForExit(3000);
            }
        }
        catch
        {
            // Best-effort cleanup for the runtime process this client started.
        }
        finally
        {
            process.Dispose();
            _runtimeProcess = null;
        }
    }

    private void OnNavigationStarting(object? sender, CoreWebView2NavigationStartingEventArgs args)
    {
        if (!Uri.TryCreate(args.Uri, UriKind.Absolute, out var uri))
        {
            return;
        }

        if (IsWindowsBrowserLoginHandoff(uri))
        {
            args.Cancel = true;
            var browserUrl = BrowserLoginHandoffUrl(uri);
            OpenInBrowser(browserUrl);
            ShowBrowserHandoff(browserUrl);
            return;
        }

        if (IsCloudDeviceLogin(uri))
        {
            args.Cancel = true;
            if (HasDeviceCode(uri))
            {
                OpenInBrowser(args.Uri);
                return;
            }

            _ = BeginCloudDeviceLoginAsync();
            return;
        }

        if (IsHostedAuthUri(uri))
        {
            args.Cancel = true;
            OpenInBrowser(args.Uri);
            ShowBrowserHandoff(args.Uri);
        }
    }

    private async Task BeginCloudDeviceLoginAsync()
    {
        _cloudLoginCancellation?.Cancel();
        _cloudLoginCancellation = new CancellationTokenSource();
        var token = _cloudLoginCancellation.Token;

        NavigateHtml(RenderDeviceLoginStarting());

        try
        {
            var code = await RequestDeviceCodeAsync(token);
            if (string.IsNullOrWhiteSpace(code.DeviceCode) || string.IsNullOrWhiteSpace(code.UserCode))
            {
                throw new InvalidOperationException("kombify Cloud did not return a complete device code response.");
            }

            var verificationUrl = BuildVerificationUrl(code);
            NavigateHtml(RenderDeviceLoginWaiting(code, verificationUrl));
            OpenInBrowser(verificationUrl);
            await PollCloudAuthorizationAsync(code, token);
        }
        catch (OperationCanceledException)
        {
            // A new cloud-login attempt replaced this one.
        }
        catch (Exception ex)
        {
            NavigateHtml(RenderFallback(
                "Cloud login could not start.",
                $"The Windows client could not create a kombify Cloud device login. {ex.Message}"));
        }
    }

    private async Task<CloudDeviceCodeResponse> RequestDeviceCodeAsync(CancellationToken token)
    {
        using var payload = JsonContent(new { tool = _config.ToolName });
        using var response = await _httpClient.PostAsync(_config.CloudDeviceCodeEndpoint, payload, token);
        var body = await response.Content.ReadAsStringAsync(token);

        if (!response.IsSuccessStatusCode)
        {
            throw new InvalidOperationException(CloudError(response.StatusCode, body));
        }

        return JsonSerializer.Deserialize<CloudDeviceCodeResponse>(body, JsonOptions)
            ?? throw new InvalidOperationException("kombify Cloud returned an empty device code response.");
    }

    private async Task PollCloudAuthorizationAsync(CloudDeviceCodeResponse code, CancellationToken token)
    {
        var interval = TimeSpan.FromSeconds(Math.Clamp(code.Interval, 3, 30));
        var timeout = DateTimeOffset.UtcNow.AddSeconds(Math.Clamp(code.ExpiresIn, 60, 1800));

        while (DateTimeOffset.UtcNow < timeout)
        {
            await Task.Delay(interval, token);

            using var payload = JsonContent(new { device_code = code.DeviceCode });
            using var response = await _httpClient.PostAsync(_config.CloudDevicePollEndpoint, payload, token);
            var body = await response.Content.ReadAsStringAsync(token);

            if (response.IsSuccessStatusCode)
            {
                SaveCloudSession(body);
                NavigateHtml(RenderDeviceLoginSuccess(_config.CloudUiUrl));
                return;
            }

            if (response.StatusCode == HttpStatusCode.Accepted
                || body.Contains("authorization_pending", StringComparison.OrdinalIgnoreCase))
            {
                continue;
            }

            if ((int)response.StatusCode == 429 || body.Contains("slow_down", StringComparison.OrdinalIgnoreCase))
            {
                interval = interval.Add(TimeSpan.FromSeconds(2));
                continue;
            }

            if (response.StatusCode == HttpStatusCode.Gone
                || body.Contains("expired_token", StringComparison.OrdinalIgnoreCase))
            {
                NavigateHtml(RenderFallback(
                    "Cloud login expired.",
                    "Start kombify Cloud login again to create a fresh device code."));
                return;
            }

            NavigateHtml(RenderFallback(
                "Cloud login failed.",
                CloudError(response.StatusCode, body)));
            return;
        }

        NavigateHtml(RenderFallback(
            "Cloud login expired.",
            "Start kombify Cloud login again to create a fresh device code."));
    }

    private static StringContent JsonContent<T>(T value)
    {
        return new StringContent(
            JsonSerializer.Serialize(value, JsonOptions),
            Encoding.UTF8,
            "application/json");
    }

    private static string BuildVerificationUrl(CloudDeviceCodeResponse code)
    {
        if (!string.IsNullOrWhiteSpace(code.VerificationUriComplete)
            && Uri.TryCreate(code.VerificationUriComplete, UriKind.Absolute, out _))
        {
            return code.VerificationUriComplete;
        }

        var baseUri = string.IsNullOrWhiteSpace(code.VerificationUri)
            ? "https://kombify.io/device"
            : code.VerificationUri;
        var separator = baseUri.Contains('?') ? '&' : '?';
        return $"{baseUri}{separator}code={Uri.EscapeDataString(code.UserCode)}";
    }

    private static bool IsCloudDeviceLogin(Uri uri)
    {
        return (uri.Host.Equals("app.kombify.io", StringComparison.OrdinalIgnoreCase)
                || uri.Host.Equals("kombify.io", StringComparison.OrdinalIgnoreCase))
            && (uri.AbsolutePath.Equals("/device", StringComparison.OrdinalIgnoreCase)
                || uri.AbsolutePath.StartsWith("/signin/start/", StringComparison.OrdinalIgnoreCase));
    }

    private static bool IsHostedAuthUri(Uri uri)
    {
        return uri.Host.Equals("app.kombify.io", StringComparison.OrdinalIgnoreCase)
            || uri.Host.Equals("kombify.io", StringComparison.OrdinalIgnoreCase)
            || uri.Host.EndsWith(".auth0.com", StringComparison.OrdinalIgnoreCase);
    }

    private static bool IsWindowsBrowserLoginHandoff(Uri uri)
    {
        return uri.Query.Contains("client=windows", StringComparison.OrdinalIgnoreCase)
            && uri.Query.Contains("open_browser=1", StringComparison.OrdinalIgnoreCase);
    }

    private static string BrowserLoginHandoffUrl(Uri uri)
    {
        var builder = new UriBuilder(uri);
        var query = uri.Query.TrimStart('?')
            .Split('&', StringSplitOptions.RemoveEmptyEntries)
            .Where(part =>
                !part.Equals("open_browser=1", StringComparison.OrdinalIgnoreCase)
                && !part.StartsWith("open_browser=", StringComparison.OrdinalIgnoreCase));
        builder.Query = string.Join("&", query);
        return builder.Uri.ToString();
    }

    private static bool HasDeviceCode(Uri uri)
    {
        return uri.Query.Contains("code=", StringComparison.OrdinalIgnoreCase);
    }

    private static bool IsHttp(string value)
    {
        return Uri.TryCreate(value, UriKind.Absolute, out var uri)
            && (uri.Scheme == Uri.UriSchemeHttp || uri.Scheme == Uri.UriSchemeHttps);
    }

    private static void OpenInBrowser(string url)
    {
        Process.Start(new ProcessStartInfo { FileName = url, UseShellExecute = true });
    }

    private void ShowBrowserHandoff(string browserUrl)
    {
        NavigateHtml(RenderFallback(
            "Continue in your browser.",
            "The kombify Cloud login was opened in your default browser so password managers and passkeys can use the normal browser session.",
            IsHttp(browserUrl)
                ? $"""<p><a href="{Html(browserUrl)}">Open browser login again</a></p>"""
                : ""));
    }

    private void SaveCloudSession(string body)
    {
        var response = JsonSerializer.Deserialize<CloudAuthorizationResponse>(body, JsonOptions)
            ?? throw new InvalidOperationException("kombify Cloud returned an empty authorization response.");
        if (string.IsNullOrWhiteSpace(response.Token.AccessToken)
            || string.IsNullOrWhiteSpace(response.Token.RefreshToken)
            || string.IsNullOrWhiteSpace(response.Token.UserId))
        {
            throw new InvalidOperationException("kombify Cloud returned an incomplete authorization response.");
        }

        var accessTarget = CloudCredentialTarget("access-token");
        var refreshTarget = CloudCredentialTarget("refresh-token");
        WindowsCredentialStore.Write(accessTarget, response.Token.UserId, response.Token.AccessToken);
        try
        {
            WindowsCredentialStore.Write(refreshTarget, response.Token.UserId, response.Token.RefreshToken);
        }
        catch
        {
            WindowsCredentialStore.Delete(accessTarget);
            throw;
        }

        Directory.CreateDirectory(ClientConfig.StateDirectory());
        var path = Path.Combine(ClientConfig.StateDirectory(), "cloud-session.json");
        var envelope = new
        {
            connectedAt = DateTimeOffset.UtcNow,
            source = "kombify-cloud-device-code",
            credentialStore = "windows-credential-manager",
            accessCredentialTarget = accessTarget,
            refreshCredentialTarget = refreshTarget,
            expiresAt = response.Token.ExpiresAt,
            userId = response.Token.UserId,
            entitlement = response.Entitlements is null
                ? null
                : new { tool = response.Entitlements.Tool, expiresAt = response.Entitlements.ExpiresAt },
        };

        File.WriteAllText(path, JsonSerializer.Serialize(envelope, new JsonSerializerOptions
        {
            WriteIndented = true,
        }));
    }

    private string CloudCredentialTarget(string credentialKind)
    {
        var tool = new string(_config.ToolName
            .ToLowerInvariant()
            .Where(character => char.IsLetterOrDigit(character) || character is '-' or '_')
            .ToArray());
        if (string.IsNullOrWhiteSpace(tool))
        {
            tool = "stack";
        }

        return ClientConfig.CredentialTarget($"kombify/techstack/cloud/{tool}/{credentialKind}");
    }

    private void NavigateHtml(string html)
    {
        if (InvokeRequired)
        {
            BeginInvoke(new Action(() => NavigateHtml(html)));
            return;
        }

        _webView.CoreWebView2.NavigateToString(html);
    }

    private static string CloudError(HttpStatusCode statusCode, string body)
    {
        var message = ExtractJsonError(body);
        return string.IsNullOrWhiteSpace(message)
            ? $"kombify Cloud returned {(int)statusCode} {statusCode}."
            : $"kombify Cloud returned {(int)statusCode} {statusCode}: {message}";
    }

    private static string ExtractJsonError(string body)
    {
        if (string.IsNullOrWhiteSpace(body))
        {
            return "";
        }

        try
        {
            using var doc = JsonDocument.Parse(body);
            var root = doc.RootElement;
            foreach (var key in new[] { "error_description", "message", "error" })
            {
                if (root.TryGetProperty(key, out var value) && value.ValueKind == JsonValueKind.String)
                {
                    return value.GetString() ?? "";
                }
            }
        }
        catch
        {
            return body.Length > 240 ? body[..240] : body;
        }

        return "";
    }

    private static string RenderDeviceLoginStarting()
    {
        return RenderShell(
            "Starting kombify Cloud login.",
            "The Windows client is creating a one-time device code with kombify Cloud.",
            "");
    }

    private static string RenderLocalRuntimeStarting(string localOrigin)
    {
        return RenderShell(
            "Starting local TechStack runtime.",
            $"The Windows client is opening the local control plane at {localOrigin}.",
            "");
    }

    private static string RenderServerBindingStarting(string serverUrl)
    {
        return RenderShell(
            "Verifying the Techstack instance.",
            $"The Windows client is loading the public connection profile from {serverUrl}.",
            "");
    }

    private static string RenderDeviceLoginWaiting(CloudDeviceCodeResponse code, string verificationUrl)
    {
        return RenderShell(
            "Continue in your browser.",
            "Sign in with kombify Cloud Universal Login and approve this Windows client.",
            $$"""
<div class="code">{{Html(code.UserCode)}}</div>
<p class="copy small">This code is valid for about {{Math.Max(1, code.ExpiresIn / 60)}} minutes. The client will continue automatically after approval.</p>
<p><a href="{{Html(verificationUrl)}}">Open kombify Cloud authorization again</a></p>
""");
    }

    private static string RenderDeviceLoginSuccess(string cloudUiUrl)
    {
        return RenderShell(
            "kombify Cloud connected.",
            "The Windows client received a Cloud tool token and stored the local session for this device.",
            IsHttp(cloudUiUrl)
                ? $"""<p><a href="{Html(cloudUiUrl)}">Open TechStack Cloud UI</a></p>"""
                : "");
    }

    private static string RenderFallback(string title, string detail, string extraHtml = "")
    {
        return RenderShell(title, detail, extraHtml);
    }

    private static string RenderShell(string title, string detail, string extraHtml)
    {
        var logo = LogoDataUri();
        return $$"""
<!doctype html>
<html><head><meta charset="utf-8"><title>{{Html(title)}}</title>
<style>
html,body{height:100%;margin:0;font-family:Segoe UI,system-ui,sans-serif;background:#f7f9fc;color:#06142d}
body{display:grid;place-items:center}.card{width:min(720px,calc(100vw - 56px));background:white;border:1px solid #d9e0ec;border-radius:10px;padding:44px;box-shadow:0 24px 70px rgba(7,22,47,.12)}
img{width:72px;height:72px;object-fit:contain;margin-bottom:26px}h1{font-size:40px;line-height:1.08;margin:0}.copy{color:#566274;font-size:17px;line-height:1.6}.copy.small{font-size:14px}
.tag{font-size:12px;color:#0b3a78;text-transform:uppercase;font-weight:700}.code{display:inline-flex;margin-top:8px;padding:14px 18px;border:1px solid #c7d4e8;border-radius:8px;background:#f3f7fc;color:#07366f;font-family:Consolas,monospace;font-size:30px;font-weight:700;letter-spacing:.12em}
a{color:#0b3a78;font-weight:700}
pre{max-height:220px;overflow:auto;background:#06142d;color:#f5f7fb;padding:14px;border-radius:8px;font-size:12px;line-height:1.45;white-space:pre-wrap}.path{font-family:Consolas,monospace;color:#25314a}
</style></head><body><main class="card"><img src="{{logo}}" alt=""><p class="tag">kombify TechStack Client</p><h1>{{Html(title)}}</h1><p class="copy">{{Html(detail)}}</p>{{extraHtml}}</main></body></html>
""";
    }

    private static string LocalRuntimeFailureDetail(string logPath, int exitCode)
    {
        var tail = Tail(logPath);
        if (tail.Contains("start embedded postgres", StringComparison.OrdinalIgnoreCase))
        {
            return "The bundled local Postgres database failed to start. Check postgres.log in the runtime data "
                + "directory; a stale postmaster.pid after a crash or an antivirus block are the most common causes.";
        }

        if (tail.Contains("Only one usage of each socket address", StringComparison.OrdinalIgnoreCase)
            || tail.Contains("address already in use", StringComparison.OrdinalIgnoreCase))
        {
            return "The local runtime could not bind its address (default 127.0.0.1:5260) because another process "
                + "is already using it. Close the conflicting process or configure a different local UI URL.";
        }

        if (tail.Contains("DATABASE_URL", StringComparison.OrdinalIgnoreCase)
            || tail.Contains("control-plane database is required", StringComparison.OrdinalIgnoreCase))
        {
            return "The local runtime could not use its control-plane database. A DATABASE_URL override points at "
                + "an unreachable Postgres or the embedded database was disabled; remove the override to use the "
                + "bundled local database.";
        }

        return $"The local runtime exited with code {exitCode}.";
    }

    private static string RuntimeLogExtraHtml(string logPath)
    {
        var tail = Tail(logPath);
        var pre = string.IsNullOrWhiteSpace(tail) ? "" : $"""<pre>{Html(tail)}</pre>""";
        return $"""<p class="copy small">Runtime log: <span class="path">{Html(logPath)}</span></p>{pre}""";
    }

    private static void AppendRuntimeLog(string path, string? line)
    {
        if (string.IsNullOrWhiteSpace(line))
        {
            return;
        }

        try
        {
            lock (RuntimeLogLock)
            {
                File.AppendAllText(path, line + Environment.NewLine, Encoding.UTF8);
            }
        }
        catch
        {
            // Runtime logging must never crash the shell.
        }
    }

    private static string Tail(string path, int maxChars = 4000)
    {
        try
        {
            if (!File.Exists(path))
            {
                return "";
            }

            var text = File.ReadAllText(path);
            return text.Length <= maxChars ? text : text[^maxChars..];
        }
        catch
        {
            return "";
        }
    }

    private static Icon? LoadIcon()
    {
        var path = Path.Combine(AppContext.BaseDirectory, "Assets", "kombify-navy.ico");
        return File.Exists(path) ? new Icon(path) : null;
    }

    private static string LogoDataUri()
    {
        var path = Path.Combine(AppContext.BaseDirectory, "Assets", "kombify-navy.png");
        return File.Exists(path)
            ? "data:image/png;base64," + Convert.ToBase64String(File.ReadAllBytes(path))
            : "";
    }

    private static string Html(string value) => System.Net.WebUtility.HtmlEncode(value);
}
