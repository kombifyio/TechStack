using Kombify.TechStack.Client;
using Kombify.Client.Shell;

AssertInstallerAuthority();
Console.WriteLine($"PASS {DesktopEdition.Name} installer authority");
await AssertCredentialRedirectRejected();
Console.WriteLine("PASS credential requests do not follow redirects");
#if CLOUD_DESKTOP
await AssertConnectAccountMismatchDenied();
Console.WriteLine("PASS Connect rejects an account mismatch before custody or request effects");
#endif
if (DesktopEdition.IsLocal)
{
    await AssertSpoofedLocalRuntimeRejected();
    Console.WriteLine("PASS spoofed local runtime cannot capture the device token");
}
if (args.Contains("--authority-only", StringComparer.Ordinal)) return 0;

if (args.Contains("--credential-roundtrip", StringComparer.Ordinal))
{
    var target = $"kombify/techstack/contract-tests/{Guid.NewGuid():N}";
    var secret = Convert.ToHexString(System.Security.Cryptography.RandomNumberGenerator.GetBytes(32)).ToLowerInvariant();
    try
    {
        WindowsCredentialStore.Write(target, Environment.UserName, secret);
        if (!string.Equals(WindowsCredentialStore.Read(target), secret, StringComparison.Ordinal))
        {
            throw new InvalidOperationException("Windows Credential Manager roundtrip changed the secret.");
        }
        Console.WriteLine("PASS Windows Credential Manager roundtrip");
    }
    finally
    {
        WindowsCredentialStore.Delete(target);
    }
}

// Connect installation key: a DPoP proof must verify under the key whose RFC 7638
// thumbprint Gateway stores as dpop_jkt, and the key must not be exportable.
{
    var keyName = $"kombify-techstack-contract-tests-{Guid.NewGuid():N}";
    try
    {
        using var key = ConnectDeviceKey.OpenOrCreate(keyName);
        var proof = key.CreateProof("POST", new Uri("https://connect.kombify.io/v1/installations?x=1"));
        var segments = proof.Split('.');
        var payload = System.Text.Json.JsonDocument.Parse(Convert.FromBase64String(
            segments[1].Replace('-', '+').Replace('_', '/').PadRight(segments[1].Length + (4 - segments[1].Length % 4) % 4, '=')));
        if (!key.VerifyProof(proof)
            || payload.RootElement.GetProperty("htu").GetString() != "https://connect.kombify.io/v1/installations"
            || key.Thumbprint.Length != 43)
        {
            throw new InvalidOperationException("Connect DPoP proof does not verify under the installation key.");
        }
        Console.WriteLine($"PASS Connect DPoP proof ({key.Custody})");
    }
    finally
    {
        ConnectDeviceKey.Delete(keyName);
    }
}

const string ValidSelfHosted = """
{
  "version":"1",
  "deployment_mode":"self_hosted",
  "base_url":"https://home.example.net/",
  "instance_id":"techstack-home-001",
  "oidc":{"issuer":"https://home.example.net/oidc/","client_id":"windows-public","scopes":["openid","profile","offline_access"],"flow":"device_authorization"},
  "capabilities":["techstack.runtime.read","techstack.runtime.write"],
  "api_versions":{"techstack":"v1"},
  "sync":{"offline_read":false,"offline_write":"disabled","etag":false,"tombstones":false}
}
""";

const string ValidLocal = """
{
  "version":"1",
  "deployment_mode":"local",
  "base_url":"http://127.0.0.1:5260/",
  "instance_id":"techstack-local-001",
  "oidc":{"issuer":"http://127.0.0.1:5260/","client_id":"techstack-local","scopes":["openid","profile"],"flow":"local_bootstrap"},
  "capabilities":["techstack.runtime.read","techstack.runtime.write"],
  "api_versions":{"techstack":"v1"},
  "sync":{"offline_read":false,"offline_write":"disabled","etag":false,"tombstones":false}
}
""";

var tests = new (string Name, Action Execute)[]
{
    ("local startup opens the device-session entry without mode selection", AssertLocalStartupEntry),
    ("self-hosted HTTPS profile", () => Parse(ValidSelfHosted, "https://home.example.net/")),
    ("local loopback profile", () => Parse(ValidLocal, "http://127.0.0.1:5260/")),
    ("remote HTTP rejected", () => MustFail(() => ClientConnectionProfileValidator.NormalizeConfiguredEndpoint("http://home.example.net/"))),
    ("mismatched base URL rejected", () => MustFail(() => Parse(ValidSelfHosted, "https://other.example.net/"))),
    ("unknown token field rejected", () => MustFail(() => Parse(ValidSelfHosted.Replace("\"sync\":", "\"access_token\":\"secret\",\"sync\":"), "https://home.example.net/"))),
    ("cloud device flow rejected", () => MustFail(() => Parse(ValidSelfHosted.Replace("\"self_hosted\"", "\"cloud\""), "https://home.example.net/"))),
    ("Local edition rejects Cloud discovery", AssertLocalEditionRejectsCloudProfile),
    ("default credential target remains stable", AssertDefaultCredentialTarget),
    ("isolated state receives stable credential namespace", AssertIsolatedCredentialNamespace),
    ("state root override falls back to product state", () => AssertRejectedStateOverride("")),
    ("state sibling override falls back to product state", () => AssertRejectedStateOverride("-outside")),
};

foreach (var test in tests)
{
    try
    {
        test.Execute();
        Console.WriteLine($"PASS {test.Name}");
    }
    catch (Exception error)
    {
        Console.Error.WriteLine($"FAIL {test.Name}: {error.Message}");
        return 1;
    }
}
return 0;

#if CLOUD_DESKTOP
static async Task AssertConnectAccountMismatchDenied()
{
    var account = new ConnectProductIdentity("account-A", "tenant-A");
    var credentialWrites = 0;
    var connectRequests = 0;
    try
    {
        await ConnectEnrollment.WithVerifiedSessionAsync(account,
            _ => Task.FromResult(account),
            _ => Task.FromResult(new Auth0NativeSession("token-B", "refresh-B", "account-B")),
            _ => credentialWrites++,
            (_, _) =>
            {
                connectRequests++;
                return Task.FromResult(true);
            }, CancellationToken.None);
    }
    catch (ConnectAccountException)
    {
        if (credentialWrites == 0 && connectRequests == 0) return;
        throw new InvalidOperationException("Mismatched Connect login changed account custody or called Connect.");
    }
    throw new InvalidOperationException("Mismatched Connect login was accepted.");
}
#endif

static void AssertLocalStartupEntry()
{
    var previous = Environment.GetEnvironmentVariable("TECHSTACK_CLIENT_STATE_DIR");
    var state = Path.Combine(
        Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData),
        "kombify", $"techstack-client-startup-{Guid.NewGuid():N}");
    try
    {
        Environment.SetEnvironmentVariable("TECHSTACK_CLIENT_STATE_DIR", state);
        AssertEntry(ClientConfig.Load([]), "http://127.0.0.1:5260/client/local?client=windows");

        Directory.CreateDirectory(state);
        foreach (var path in new[] { "onboarding", "local" })
        {
            File.WriteAllText(ClientConfig.ConfigPath(),
                $$"""{"mode":"local","localOnboardingUrl":"http://127.0.0.1:5260/client/{{path}}?client=windows"}""");
            AssertEntry(ClientConfig.Load([]), "http://127.0.0.1:5260/client/local?client=windows");
        }

        MustRejectLocalCloud(["--cloud-ui-url", "https://cloud.example.test/"]);
        MustRejectLocalCloud(["--mode", "cloud"]);
        MustRejectLocalCloud(["--connect-enroll"]);
        AssertEntry(ClientConfig.Load(["--url", "https://home.example.test/"]),
            "https://home.example.test/");
        AssertEntry(ClientConfig.Load(["--local-ui-url", "http://127.0.0.1:6270/", "--local-onboarding-url", "http://127.0.0.1:6270/client/local?client=windows"]),
            "http://127.0.0.1:6270/client/local?client=windows");
    }
    finally
    {
        Environment.SetEnvironmentVariable("TECHSTACK_CLIENT_STATE_DIR", previous);
        if (File.Exists(Path.Combine(state, "client.json"))) File.Delete(Path.Combine(state, "client.json"));
        if (Directory.Exists(state)) Directory.Delete(state);
    }
}

static void AssertEntry(ClientConfig config, string expected)
{
    if (!string.Equals(config.InitialUrl(), expected, StringComparison.Ordinal))
    {
        throw new InvalidOperationException($"Client opened {config.InitialUrl()} instead of {expected}.");
    }
}

static void AssertInstallerAuthority()
{
    var previous = Environment.GetEnvironmentVariable("TECHSTACK_CLIENT_STATE_DIR");
    var root = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "kombify");
    var state = DesktopEdition.IsLocal
        ? Path.Combine(root, $"techstack-client-contract-{Guid.NewGuid():N}")
        : Path.Combine(root, "techstack", "cloud", "beta", $"contract-{Guid.NewGuid():N}");
    try
    {
        Environment.SetEnvironmentVariable("TECHSTACK_CLIENT_STATE_DIR", state);
        Directory.CreateDirectory(state);
        var rejected = DesktopEdition.IsLocal
            ? new[] {
                """{"localUiUrl":"https://remote.example/"}""",
                """{"localOnboardingUrl":"http://127.0.0.1:6270/"}""",
                """{"localUiUrl":"http://user@127.0.0.1:5260/"}"""
            }
            : new[] {
                """{"cloudUiUrl":"http://127.0.0.1:5260/"}""",
                """{"cloudUiUrl":"https://techstack.kombify.io.evil.example/"}""",
                """{"connectAuth0Issuer":"https://other.example/"}""",
                """{"cloudDevicePollEndpoint":"https://other.example/poll"}""",
                """{"cloudDevicePollEndpoint":"https://app.kombify.io/other"}"""
            };
        foreach (var json in rejected)
        {
            File.WriteAllText(ClientConfig.ConfigPath(), json);
            try { ClientConfig.Load([]); }
            catch (InvalidOperationException) { continue; }
            throw new InvalidOperationException("A persisted endpoint escaped the installer authority.");
        }
        File.Delete(ClientConfig.ConfigPath());
        var config = ClientConfig.Load([]);
        if (!ClientAuthority.AllowsProductNavigation(new Uri(config.InitialUrl()), config.InitialUrl())
            || ClientAuthority.AllowsProductNavigation(new Uri("https://other.example/"), config.InitialUrl())
            || (!DesktopEdition.IsLocal && ClientAuthority.AllowsProductNavigation(new Uri("http://127.0.0.1:5260/"), config.InitialUrl())))
            throw new InvalidOperationException("Navigation did not preserve the installed product authority.");
        var arguments = DesktopEdition.IsLocal
            ? new[] { "--local-ui-url", "https://other.example/" }
            : new[] { "--cloud-ui-url", "http://127.0.0.1:5260/" };
        try { ClientConfig.Load(arguments); }
        catch (InvalidOperationException) { return; }
        throw new InvalidOperationException("A command-line endpoint escaped the installer authority.");
    }
    finally
    {
        Environment.SetEnvironmentVariable("TECHSTACK_CLIENT_STATE_DIR", previous);
        if (File.Exists(Path.Combine(state, "client.json"))) File.Delete(Path.Combine(state, "client.json"));
        if (Directory.Exists(state)) Directory.Delete(state);
    }
}

static async Task AssertCredentialRedirectRejected()
{
    var reservation = new System.Net.Sockets.TcpListener(System.Net.IPAddress.Loopback, 0);
    reservation.Start();
    var port = ((System.Net.IPEndPoint)reservation.LocalEndpoint).Port;
    reservation.Stop();
    using var listener = new System.Net.HttpListener();
    var origin = $"http://127.0.0.1:{port}/";
    listener.Prefixes.Add(origin);
    listener.Start();
    using var http = ClientAuthority.CreateHttpClient();
    using var timeout = new CancellationTokenSource(TimeSpan.FromSeconds(5));
    using var request = new HttpRequestMessage(HttpMethod.Post, origin + "bootstrap");
    request.Headers.Add("X-TechStack-Device-Token", "isolated-test-token");
    var responseTask = http.SendAsync(request, timeout.Token);
    var context = await listener.GetContextAsync().WaitAsync(timeout.Token);
    context.Response.StatusCode = 307;
    context.Response.RedirectLocation = origin + "credential-sink";
    context.Response.Close();
    using var response = await responseTask;
    if (response.StatusCode != System.Net.HttpStatusCode.TemporaryRedirect)
        throw new InvalidOperationException("The credential-bearing request followed a redirect.");
}

static async Task AssertSpoofedLocalRuntimeRejected()
{
    var reservation = new System.Net.Sockets.TcpListener(System.Net.IPAddress.Loopback, 0);
    reservation.Start();
    var port = ((System.Net.IPEndPoint)reservation.LocalEndpoint).Port;
    reservation.Stop();
    using var listener = new System.Net.HttpListener();
    var origin = $"http://127.0.0.1:{port}/";
    listener.Prefixes.Add(origin);
    listener.Start();
    using var http = ClientAuthority.CreateHttpClient();
    using var timeout = new CancellationTokenSource(TimeSpan.FromSeconds(5));
    var secret = new string('a', 64);
    var authentication = LocalDeviceSessionProof.AuthenticateAsync(http, new Uri(origin), secret, timeout.Token);
    var context = await listener.GetContextAsync().WaitAsync(timeout.Token);
    if (context.Request.Url?.AbsolutePath != LocalDeviceSessionProof.ChallengePath
        || context.Request.Headers["X-TechStack-Device-Token"] is not null
        || context.Request.Headers["X-TechStack-Device-Proof"] is not null)
        throw new InvalidOperationException("A spoof listener received the raw credential or a session proof before authentication.");
    context.Response.ContentType = "application/json";
    var fake = System.Text.Encoding.UTF8.GetBytes(
        "{\"data\":{\"server_nonce\":\"" + new string('b', 32) + "\",\"proof\":\"" + new string('c', 64) + "\"}}");
    context.Response.OutputStream.Write(fake);
    context.Response.Close();
    try { await authentication; }
    catch (InvalidOperationException) { return; }
    throw new InvalidOperationException("A spoof listener passed local runtime identity verification.");
}

static void MustRejectLocalCloud(string[] arguments)
{
    try { ClientConfig.Load(arguments); }
    catch (InvalidOperationException) { return; }
    throw new InvalidOperationException("The Local client accepted a Cloud or Connect entry point.");
}

static void AssertLocalEditionRejectsCloudProfile()
{
    var cloud = ValidSelfHosted.Replace("\"self_hosted\"", "\"cloud\"")
        .Replace("\"device_authorization\"", "\"authorization_code_pkce\"");
    var profile = Parse(cloud, "https://home.example.net/");
    MustFail(() => ClientConfig.ValidateProfileAuthority(profile));
}

static ClientConnectionProfile Parse(string json, string endpoint)
{
    return ClientConnectionProfileValidator.ParseAndValidate(
        json,
        ClientConnectionProfileValidator.NormalizeConfiguredEndpoint(endpoint));
}

static void MustFail(Action action)
{
    try
    {
        action();
    }
    catch (ClientProfileException)
    {
        return;
    }
    throw new InvalidOperationException("Expected the profile to fail closed.");
}

static void AssertDefaultCredentialTarget()
{
    const string baseTarget = "kombify/techstack/local/device-session-token";
    var previous = Environment.GetEnvironmentVariable("TECHSTACK_CLIENT_STATE_DIR");
    try
    {
        Environment.SetEnvironmentVariable("TECHSTACK_CLIENT_STATE_DIR", null);
        if (!string.Equals(ClientConfig.CredentialTarget(baseTarget), baseTarget, StringComparison.Ordinal))
        {
            throw new InvalidOperationException("Default product credential target changed unexpectedly.");
        }
    }
    finally
    {
        Environment.SetEnvironmentVariable("TECHSTACK_CLIENT_STATE_DIR", previous);
    }
}

static void AssertIsolatedCredentialNamespace()
{
    const string baseTarget = "kombify/techstack/local/device-session-token";
    var previous = Environment.GetEnvironmentVariable("TECHSTACK_CLIENT_STATE_DIR");
    try
    {
        var root = Path.Combine(
            Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData),
            "kombify");
        var firstState = Path.Combine(root, "techstack-client-contract-a");
        var secondState = Path.Combine(root, "techstack-client-contract-b");

        Environment.SetEnvironmentVariable("TECHSTACK_CLIENT_STATE_DIR", firstState);
        var first = ClientConfig.CredentialTarget(baseTarget);
        var replay = ClientConfig.CredentialTarget(baseTarget);
        Environment.SetEnvironmentVariable("TECHSTACK_CLIENT_STATE_DIR", secondState);
        var second = ClientConfig.CredentialTarget(baseTarget);

        if (!first.StartsWith($"{baseTarget}/state-", StringComparison.Ordinal)
            || first.Length != baseTarget.Length + "/state-".Length + 16
            || !string.Equals(first, replay, StringComparison.Ordinal)
            || string.Equals(first, second, StringComparison.Ordinal))
        {
            throw new InvalidOperationException("State-scoped credential target is missing, unstable, or colliding.");
        }
    }
    finally
    {
        Environment.SetEnvironmentVariable("TECHSTACK_CLIENT_STATE_DIR", previous);
    }
}

static void AssertRejectedStateOverride(string rootSuffix)
{
    var previous = Environment.GetEnvironmentVariable("TECHSTACK_CLIENT_STATE_DIR");
    try
    {
        var localAppData = Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData);
        var rejected = Path.Combine(localAppData, $"kombify{rootSuffix}");
        var expected = Path.Combine(localAppData, "kombify", "techstack-client");
        Environment.SetEnvironmentVariable("TECHSTACK_CLIENT_STATE_DIR", rejected);
        if (!string.Equals(ClientConfig.StateDirectory(), expected, StringComparison.OrdinalIgnoreCase))
        {
            throw new InvalidOperationException($"Unsafe state override was accepted: {rejected}");
        }
    }
    finally
    {
        Environment.SetEnvironmentVariable("TECHSTACK_CLIENT_STATE_DIR", previous);
    }
}
