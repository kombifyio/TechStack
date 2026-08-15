using Kombify.TechStack.Client;

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
    ("self-hosted HTTPS profile", () => Parse(ValidSelfHosted, "https://home.example.net/")),
    ("local loopback profile", () => Parse(ValidLocal, "http://127.0.0.1:5260/")),
    ("remote HTTP rejected", () => MustFail(() => ClientConnectionProfileValidator.NormalizeConfiguredEndpoint("http://home.example.net/"))),
    ("mismatched base URL rejected", () => MustFail(() => Parse(ValidSelfHosted, "https://other.example.net/"))),
    ("unknown token field rejected", () => MustFail(() => Parse(ValidSelfHosted.Replace("\"sync\":", "\"access_token\":\"secret\",\"sync\":"), "https://home.example.net/"))),
    ("cloud device flow rejected", () => MustFail(() => Parse(ValidSelfHosted.Replace("\"self_hosted\"", "\"cloud\""), "https://home.example.net/"))),
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
