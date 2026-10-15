using Kombify.TechStack.Client;
using Kombify.Client.Shell;

if (args.Contains("--wallet-live-probe", StringComparer.Ordinal))
{
    if (!DesktopEdition.IsLocal) throw new InvalidOperationException("Wallet device proof belongs to the Local installer.");
    await WindowsLocalWalletDeviceRevealBoundary(args);
    return 0;
}

if (args.Contains("--handoff-live-probe", StringComparer.Ordinal))
{
    await ObserveInstallerHandoffAsync();
    return 0;
}

if (args.Contains("--rollback-only", StringComparer.Ordinal))
{
    if (!DesktopEdition.IsLocal) throw new InvalidOperationException("Rollback belongs to the Local installer.");
    AssertLegacyRollbackResumesWithVerifierEvidence();
    AssertFailedUpdatePreservesRecovery();
    AssertPendingRollbackResumesBeforeStartup();
    Console.WriteLine("PASS pending rollback protects and restores data before runtime startup");
    return 0;
}

AssertInstallerAuthority();
Console.WriteLine($"PASS {DesktopEdition.Name} installer authority");
AssertDeviceContextDocumentBoundary();
Console.WriteLine("PASS device context stays inside the current product document and navigation lifetime");
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
if (DesktopEdition.IsLocal)
{
    AssertLegacyRollbackResumesWithVerifierEvidence();
    AssertFailedUpdatePreservesRecovery();
    AssertPendingRollbackResumesBeforeStartup();
    Console.WriteLine("PASS pending rollback protects and restores data before runtime startup");
}

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

// This development probe uses the real coordinator with owned harmless
// executables. It performs no installation, elevation or real state reset.
static async Task ObserveInstallerHandoffAsync()
{
    var root = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "kombify");
    var state = Path.Combine(root, $"techstack-client-contract-{Guid.NewGuid():N}");
    Directory.CreateDirectory(state);
    System.Diagnostics.Process? coordinator = null;
    try
    {
        var source = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.System), "cmd.exe");
        var first = Path.Combine(state, "first.exe");
        var second = Path.Combine(state, "second.exe");
        File.Copy(source, first);
        File.Copy(source, second);
        var digest = Convert.ToHexString(System.Security.Cryptography.SHA256.HashData(File.ReadAllBytes(source)));
        var size = new FileInfo(source).Length;
        var entered = Path.Combine(state, "entered");
        var continued = Path.Combine(state, "continued");
        var start = ClientUpdater.InstallerHandoffStartInfo([
            new ClientUpdater.InstallerHandoff(first, digest, size,
                "/d /c \"echo entered>\"%KOMBIFY_HANDOFF_PROBE_ENTERED%\" & ping -n 7 127.0.0.1 >nul & exit /b 23\""),
            new ClientUpdater.InstallerHandoff(second, digest, size,
                "/d /c \"echo continued>\"%KOMBIFY_HANDOFF_PROBE_CONTINUED%\" & exit /b 0\"")],
            Path.Combine(state, "never-restart.exe"), Path.Combine(state, "handoff.log"), 0);
        start.Environment["KOMBIFY_HANDOFF_PROBE_ENTERED"] = entered;
        start.Environment["KOMBIFY_HANDOFF_PROBE_CONTINUED"] = continued;
        coordinator = System.Diagnostics.Process.Start(start) ?? throw new InvalidOperationException("The handoff probe did not start.");
        var deadline = DateTime.UtcNow.AddSeconds(20);
        while (!File.Exists(entered) && !coordinator.HasExited && DateTime.UtcNow < deadline)
            await Task.Delay(50);
        if (!File.Exists(entered)) throw new InvalidOperationException("The harmless installer did not reach execution.");
        foreach (var path in new[] { first, second })
        {
            var writeDenied = false;
            var deleteDenied = false;
            try { File.WriteAllText(path, "substituted installer"); } catch (IOException) { writeDenied = true; }
            try { File.Delete(path); } catch (IOException) { deleteDenied = true; }
            if (!writeDenied || !deleteDenied)
                throw new InvalidOperationException("An installer could be replaced or deleted during handoff.");
        }
        using var budget = new CancellationTokenSource(TimeSpan.FromSeconds(20));
        await coordinator.WaitForExitAsync(budget.Token);
        if (coordinator.ExitCode == 0 || File.Exists(continued))
            throw new InvalidOperationException("A failed installer allowed continuation or restart.");
        Console.WriteLine("Observed installer handoff: both packages denied write/delete; failed installer stopped continuation and restart.");
    }
    finally
    {
        if (coordinator is not null)
        {
            if (!coordinator.HasExited) { coordinator.Kill(entireProcessTree: true); await coordinator.WaitForExitAsync(); }
            coordinator.Dispose();
        }
        if (!Path.GetFullPath(state).StartsWith(Path.GetFullPath(root) + Path.DirectorySeparatorChar, StringComparison.OrdinalIgnoreCase))
            throw new InvalidOperationException("The handoff probe escaped its owned state root.");
        Directory.Delete(state, recursive: true);
    }
}

// The Go verifier's cryptographic boundary is exercised independently. This
// module probe consumes its approved results and observes the durable migration
// and handoff, without launching any installer or supplying a runtime key.
static void AssertLegacyRollbackResumesWithVerifierEvidence()
{
    var previous = Environment.GetEnvironmentVariable("TECHSTACK_CLIENT_STATE_DIR");
    var state = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData),
        "kombify", $"techstack-client-contract-{Guid.NewGuid():N}");
    var listener = new System.Net.Sockets.TcpListener(System.Net.IPAddress.Loopback, 0);
    try
    {
        Environment.SetEnvironmentVariable("TECHSTACK_CLIENT_STATE_DIR", state);
        listener.Start();
        var port = ((System.Net.IPEndPoint)listener.LocalEndpoint).Port;
        listener.Stop();
        var config = new ClientConfig { AutoUpdate = false, LocalUiUrl = $"http://127.0.0.1:{port}/" };
        var updates = ClientUpdater.UpdatesDirectory();
        var snapshot = Path.Combine(updates, "snapshot");
        Directory.CreateDirectory(snapshot);
        var originalData = System.Security.Cryptography.RandomNumberGenerator.GetBytes(32);
        File.WriteAllBytes(Path.Combine(snapshot, "operator-data"), originalData);
        Directory.CreateDirectory(config.RuntimeDataDir);
        var migratedData = System.Security.Cryptography.RandomNumberGenerator.GetBytes(32);
        File.WriteAllBytes(Path.Combine(config.RuntimeDataDir, "operator-data"), migratedData);
        var pending = Path.Combine(updates, "pending.json");
        var untrusted = Path.Combine(state, "legacy-untrusted.exe");
        var untrustedBytes = System.Security.Cryptography.RandomNumberGenerator.GetBytes(32);
        File.WriteAllBytes(untrusted, untrustedBytes);
        File.WriteAllText(pending, System.Text.Json.JsonSerializer.Serialize(new
        {
            from = "0.0.0-0", to = ClientUpdater.CurrentVersion(), phase = "rolling-back",
            installer = untrusted, rollbackInstaller = untrusted,
        }));
        var oldCursor = File.ReadAllBytes(pending);
        var approved = false;
        var verifierReached = false;
        var launched = false;
        ClientUpdater.VerifiedPackage Resolve(ClientConfig _, string root, string version)
        {
            verifierReached = true;
            if (!approved) throw new InvalidDataException("The historical signed manifest is not available yet.");
            var directory = Path.Combine(root, version);
            Directory.CreateDirectory(directory);
            var path = Path.Combine(directory, "package");
            var bytes = System.Text.Encoding.UTF8.GetBytes("verifier-approved release " + version);
            File.WriteAllBytes(path, bytes);
            return new ClientUpdater.VerifiedPackage { Status = "verified-rollback", Version = version,
                PackagePath = path, PackageSize = bytes.Length,
                PackageSha256 = Convert.ToHexString(System.Security.Cryptography.SHA256.HashData(bytes)) };
        }
        void Launch(ClientUpdater.InstallerHandoff[] installers)
        {
            launched = true;
            using var cursor = System.Text.Json.JsonDocument.Parse(File.ReadAllBytes(pending));
            foreach (var installer in installers)
            {
                if (Path.GetFullPath(installer.Path).Equals(Path.GetFullPath(untrusted), StringComparison.OrdinalIgnoreCase))
                    throw new InvalidOperationException("Legacy paths authorized the resumed handoff.");
                var bytes = File.ReadAllBytes(installer.Path);
                var hash = Convert.ToHexString(System.Security.Cryptography.SHA256.HashData(bytes));
                var isRecovery = installer.Arguments == "/passive /norestart";
                var storedHash = cursor.RootElement.GetProperty(isRecovery ? "rollbackSha256" : "installerSha256").GetString();
                var storedPath = cursor.RootElement.GetProperty(isRecovery ? "rollbackInstaller" : "installer").GetString();
                if (!hash.Equals(installer.Sha256, StringComparison.OrdinalIgnoreCase) || bytes.Length != installer.Size ||
                    !hash.Equals(storedHash, StringComparison.OrdinalIgnoreCase) || storedPath != installer.Path)
                    throw new InvalidOperationException("Installer handoff preceded durable verifier-derived migration.");
            }
        }
        if (ClientUpdater.ResumePendingRollback(config, out var blocked, Resolve, Launch) || blocked is null || launched ||
            !File.ReadAllBytes(pending).SequenceEqual(oldCursor))
            throw new InvalidOperationException("Unavailable signatures lost or authorized legacy recovery.");
        listener = new System.Net.Sockets.TcpListener(System.Net.IPAddress.Loopback, port);
        listener.Start();
        approved = true;
        verifierReached = false;
        if (ClientUpdater.ResumePendingRollback(config, out blocked, Resolve, Launch) || blocked is null || verifierReached || launched)
            throw new InvalidOperationException("Legacy migration crossed the running-runtime boundary.");
        listener.Stop();
        if (!ClientUpdater.ResumePendingRollback(config, out blocked, Resolve, Launch) || blocked is not null || !launched)
            throw new InvalidOperationException("Available verifier evidence did not resume legacy recovery.");
        if (!File.ReadAllBytes(untrusted).SequenceEqual(untrustedBytes) ||
            !File.ReadAllBytes(Path.Combine(snapshot, "operator-data")).SequenceEqual(originalData) ||
            !File.ReadAllBytes(Path.Combine(config.RuntimeDataDir, "operator-data")).SequenceEqual(migratedData))
            throw new InvalidOperationException("Artifact migration changed retained data before installation recovery.");
        Console.WriteLine("Observed legacy rollback: missing signatures retained recovery; later verifier evidence durably bound canonical packages and resumed handoff.");
    }
    finally
    {
        listener.Stop();
        Environment.SetEnvironmentVariable("TECHSTACK_CLIENT_STATE_DIR", previous);
        if (Directory.Exists(state)) Directory.Delete(state, recursive: true);
    }
}

static void AssertFailedUpdatePreservesRecovery()
{
    var previous = Environment.GetEnvironmentVariable("TECHSTACK_CLIENT_STATE_DIR");
    var state = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData),
        "kombify", $"techstack-client-contract-{Guid.NewGuid():N}");
    try
    {
        Environment.SetEnvironmentVariable("TECHSTACK_CLIENT_STATE_DIR", state);
        var config = new ClientConfig { AutoUpdate = false };
        var updates = ClientUpdater.UpdatesDirectory();
        var snapshot = Path.Combine(updates, "snapshot");
        var pending = Path.Combine(updates, "pending.json");
        Directory.CreateDirectory(snapshot);
        File.WriteAllText(Path.Combine(snapshot, "operator-data.txt"), "pre-update-data");
        Directory.CreateDirectory(config.RuntimeDataDir);
        var currentData = Path.Combine(config.RuntimeDataDir, "operator-data.txt");
        File.WriteAllText(currentData, "newer-runtime-data");
        File.WriteAllText(pending, System.Text.Json.JsonSerializer.Serialize(new
        {
            from = "0.0.0", to = ClientUpdater.CurrentVersion(), phase = "applying",
            installer = Path.Combine(updates, "unavailable-new-installer.exe"),
            rollbackInstaller = Path.Combine(updates, "unavailable-previous-installer.exe"),
        }));
        var stopped = false;
        ClientUpdater.CompletePendingUpdate(config, false, () => stopped = true);
        Console.WriteLine($"Observed failed-update recovery: cursor retained={File.Exists(pending)}, runtime stopped={stopped}");
        if (!File.Exists(pending) || !stopped)
            throw new InvalidOperationException("A failed update without its recovery installer lost recovery or kept its runtime running.");
        if (ClientUpdater.TryApplyStagedUpdate(config, out var blocked) || string.IsNullOrWhiteSpace(blocked))
            throw new InvalidOperationException("An unrecoverable failed update permitted runtime startup.");
        if (File.ReadAllText(currentData) != "newer-runtime-data" ||
            File.ReadAllText(Path.Combine(snapshot, "operator-data.txt")) != "pre-update-data")
            throw new InvalidOperationException("An unrecoverable failed update changed retained recovery data.");
    }
    finally
    {
        Environment.SetEnvironmentVariable("TECHSTACK_CLIENT_STATE_DIR", previous);
        if (Directory.Exists(state)) Directory.Delete(state, recursive: true);
    }
}

static void AssertPendingRollbackResumesBeforeStartup()
{
    var previous = Environment.GetEnvironmentVariable("TECHSTACK_CLIENT_STATE_DIR");
    var state = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData),
        "kombify", $"techstack-client-contract-{Guid.NewGuid():N}");
    var listener = new System.Net.Sockets.TcpListener(System.Net.IPAddress.Loopback, 0);
    using var process = new System.Diagnostics.Process();
    try
    {
        Environment.SetEnvironmentVariable("TECHSTACK_CLIENT_STATE_DIR", state);
        listener.Start();
        var port = ((System.Net.IPEndPoint)listener.LocalEndpoint).Port;
        listener.Stop();
        var config = new ClientConfig { AutoUpdate = false, LocalUiUrl = $"http://127.0.0.1:{port}/" };
        var updates = ClientUpdater.UpdatesDirectory();
        var snapshot = Path.Combine(updates, "snapshot");
        var pending = Path.Combine(updates, "pending.json");
        var dataFile = Path.Combine(config.RuntimeDataDir, "operator-data.txt");
        Directory.CreateDirectory(config.RuntimeDataDir);
        Directory.CreateDirectory(snapshot);
        File.WriteAllText(dataFile, "newer-runtime-data");
        File.WriteAllText(Path.Combine(snapshot, "operator-data.txt"), "pre-update-data");

        void SetPending(string from) => File.WriteAllText(pending,
            System.Text.Json.JsonSerializer.Serialize(new { from, to = "0.0.1", phase = "rolling-back" }));
        void AssertDeferred(bool malformed = false)
        {
            var retainedCursor = File.ReadAllBytes(pending);
            if (ClientUpdater.TryApplyStagedUpdate(config, out var reason) || string.IsNullOrWhiteSpace(reason))
                throw new InvalidOperationException("An unfinished rollback permitted runtime startup.");
            ClientUpdater.CompletePendingUpdate(config, true,
                () => throw new InvalidOperationException("Recovery attempted to stop a runtime it does not own."));
            if (!retainedCursor.SequenceEqual(File.ReadAllBytes(pending)))
                throw new InvalidOperationException("A deferred recovery changed its retained cursor.");
            if (malformed) return;
            using var recovery = System.Text.Json.JsonDocument.Parse(File.ReadAllText(pending));
            if (recovery.RootElement.GetProperty("phase").GetString() != "rolling-back")
                throw new InvalidOperationException("Deferred recovery lost its rollback state.");
        }
        void AssertNewDataRetained()
        {
            if (File.ReadAllText(dataFile) != "newer-runtime-data")
                throw new InvalidOperationException("A blocked recovery changed data still owned by the newer runtime.");
        }

        File.WriteAllText(pending, "{\"phase\":\"rolling-back\""); // Interrupted old cursor write.
        AssertDeferred(malformed: true);
        AssertNewDataRetained();
        SetPending("0.0.0"); // The installer has not restored the previous version yet.
        AssertDeferred();
        AssertNewDataRetained();
        SetPending(ClientUpdater.CurrentVersion());
        var retainedSnapshot = Path.Combine(updates, "retained-snapshot");
        Directory.Move(snapshot, retainedSnapshot);
        AssertDeferred();
        AssertNewDataRetained();
        Directory.Move(retainedSnapshot, snapshot);

        // A harmless owned process exercises the production PID/name check.
        // It waits on redirected stdin and never runs database code.
        var probeDirectory = Path.Combine(state, "process-probe");
        Directory.CreateDirectory(probeDirectory);
        var probeExecutable = Path.Combine(probeDirectory, "postgres.exe");
        File.Copy(Environment.GetEnvironmentVariable("ComSpec")!, probeExecutable);
        var start = new System.Diagnostics.ProcessStartInfo(probeExecutable)
        {
            UseShellExecute = false, CreateNoWindow = true,
            RedirectStandardInput = true, RedirectStandardOutput = true, RedirectStandardError = true,
        };
        foreach (var argument in new[] { "/d", "/c", "set /p KOMBIFY_TEST_WAIT=" }) start.ArgumentList.Add(argument);
        process.StartInfo = start;
        if (!process.Start()) throw new InvalidOperationException("The owned observation process did not start.");
        var postgresData = Path.Combine(config.RuntimeDataDir, "postgres", "data");
        Directory.CreateDirectory(postgresData);
        File.WriteAllText(Path.Combine(postgresData, "postmaster.pid"), process.Id.ToString());
        AssertDeferred();
        AssertNewDataRetained();
        process.StandardInput.WriteLine("done");
        process.StandardInput.Close();
        if (!process.WaitForExit(3000)) throw new InvalidOperationException("The owned observation process did not stop.");

        listener = new System.Net.Sockets.TcpListener(System.Net.IPAddress.Loopback, port);
        listener.Start();
        AssertDeferred();
        AssertNewDataRetained();
        listener.Stop();

        // Windows sharing denial interrupts a real copy; the next start must
        // retry using the same retained recovery data, rather than forget it.
        using (var locked = new FileStream(Path.Combine(snapshot, "operator-data.txt"),
                   FileMode.Open, FileAccess.ReadWrite, FileShare.None)) AssertDeferred();
        if (File.ReadAllText(Path.Combine(snapshot, "operator-data.txt")) != "pre-update-data")
            throw new InvalidOperationException("A failed restore changed its recovery data.");
        if (ClientUpdater.TryApplyStagedUpdate(config, out var blocked) || blocked is not null ||
            File.ReadAllText(dataFile) != "pre-update-data")
            throw new InvalidOperationException("Recovery did not restore the pre-update data before allowing startup.");
        File.WriteAllText(dataFile, "data-written-after-recovery");
        if (ClientUpdater.TryApplyStagedUpdate(config, out blocked) || blocked is not null ||
            File.ReadAllText(dataFile) != "data-written-after-recovery")
            throw new InvalidOperationException("A committed restore blocked startup or replayed over newer data.");
    }
    finally
    {
        listener.Stop();
        try { if (!process.HasExited) { process.Kill(entireProcessTree: true); process.WaitForExit(3000); } }
        catch (InvalidOperationException) { }
        Environment.SetEnvironmentVariable("TECHSTACK_CLIENT_STATE_DIR", previous);
        if (Directory.Exists(state)) Directory.Delete(state, recursive: true);
    }
}

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

static void AssertDeviceContextDocumentBoundary()
{
    var selected = DesktopEdition.IsLocal ? "http://127.0.0.1:5260/" : ClientAuthority.CloudOrigin;
    var document = selected + "settings";
    var replies = 0;
    void Deliver(string source, string current, long revision, bool navigating, string token = "current-document")
    {
        if (ClientAuthority.AllowsDeviceContextReply(source, current, selected, 4, revision, token, "current-document", navigating))
            replies++;
    }
    Deliver("https://other.example/settings", document, 4, false);
    Deliver(document, "https://other.example/settings", 4, false);
    Deliver(selected + "dashboard", document, 4, false);
    Deliver(document, document, 5, false); // Same URL after a new navigation.
    Deliver(document, document, 4, true);
    Deliver(document, document, 4, false, "previous-document");
    if (replies != 0) throw new InvalidOperationException("A foreign or replaced document received device context.");
    Deliver(document, document, 4, false);
    if (replies != 1) throw new InvalidOperationException("The current product document could not read its context.");
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

// Opt-in focused production API/store consumer. No WebView, provider or hosted
// authority is involved; the runtime, state and credential targets are owned.
static async Task WindowsLocalWalletDeviceRevealBoundary(string[] arguments)
{
    string RequiredPath(string name) => Path.GetFullPath(arguments.Single(value => value.StartsWith(name + "=", StringComparison.Ordinal))[(name.Length + 1)..]);
    var executable = RequiredPath("--runtime-exe");
    var bundle = RequiredPath("--postgres-bundle");
    var expectedRevision = arguments.FirstOrDefault(value => value.StartsWith("--source-sha=", StringComparison.Ordinal))?[13..];
    if (!File.Exists(executable) || !Directory.Exists(bundle)) throw new InvalidOperationException("Wallet probe runtime/bundle unavailable.");
    var previousState = Environment.GetEnvironmentVariable("TECHSTACK_CLIENT_STATE_DIR");
    Environment.SetEnvironmentVariable("TECHSTACK_CLIENT_STATE_DIR", null);
    var root = ClientConfig.StateDirectory();
    var state = Path.Combine(root, "wallet-live-probe-" + Guid.NewGuid().ToString("N"));
    Environment.SetEnvironmentVariable("TECHSTACK_CLIENT_STATE_DIR", state);
    var credentialTargets = new List<string>();
    using var runtime = new SupervisedProcess();
    var dataDirectory = Path.Combine(state, "runtime");
    var origin = new Uri("http://127.0.0.1:" + WalletProbePort() + "/");
    var postgresPort = WalletProbePort();
    try
    {
        if (ClientConfig.StateDirectory() != state) throw new InvalidOperationException("Wallet probe state is not isolated.");
        Directory.CreateDirectory(state);
        File.WriteAllText(Path.Combine(state, "techstack.yaml"), "{}");
        var ownBundle = Path.Combine(state, "postgres-bundle");
        Directory.CreateDirectory(ownBundle);
        using var manifest = System.Text.Json.JsonDocument.Parse(File.ReadAllText(Path.Combine(bundle, "bundle.json")));
        var archive = manifest.RootElement.GetProperty("archive").GetString()!;
        if (Path.GetFileName(archive) != archive) throw new InvalidOperationException("Unsafe bundle archive.");
        foreach (var file in new[] { "bundle.json", "COPYRIGHT", archive }) File.Copy(Path.Combine(bundle, file), Path.Combine(ownBundle, file));
        using (var stream = File.OpenRead(Path.Combine(ownBundle, archive)))
            if (Convert.ToHexString(System.Security.Cryptography.SHA256.HashData(stream)).ToLowerInvariant()
                != manifest.RootElement.GetProperty("archive_sha256").GetString()) throw new InvalidOperationException("Copied PostgreSQL bundle integrity failed.");
        string Credential(string name, string value)
        {
            var target = ClientConfig.CredentialTarget("kombify/techstack/local/" + name);
            credentialTargets.Add(target);
            WindowsCredentialStore.Write(target, Environment.UserName, value);
            return WindowsCredentialStore.Read(target) ?? throw new InvalidOperationException("Wallet probe custody unavailable.");
        }
        var device = Credential("device-session-token", Convert.ToHexString(System.Security.Cryptography.RandomNumberGenerator.GetBytes(32)).ToLowerInvariant());
        var signing = Credential("runtime-session-secret", Convert.ToHexString(System.Security.Cryptography.RandomNumberGenerator.GetBytes(32)).ToLowerInvariant());
        var encryption = Credential("runtime-encryption-key", Convert.ToBase64String(System.Security.Cryptography.RandomNumberGenerator.GetBytes(24)).Replace('+', '-').Replace('/', '_'));
        async Task Start()
        {
            var start = new System.Diagnostics.ProcessStartInfo(executable) { WorkingDirectory = state, CreateNoWindow = true };
            foreach (var name in start.Environment.Keys.ToArray())
                if (name.StartsWith("TECHSTACK_", StringComparison.OrdinalIgnoreCase) || name.StartsWith("KOMBIFY_", StringComparison.OrdinalIgnoreCase)
                    || name.StartsWith("PG", StringComparison.OrdinalIgnoreCase) || name.StartsWith("AUTH0_", StringComparison.OrdinalIgnoreCase)
                    || name.StartsWith("IONOS_", StringComparison.OrdinalIgnoreCase) || name.StartsWith("CENTRON_", StringComparison.OrdinalIgnoreCase)
                    || name.StartsWith("SENTRY_", StringComparison.OrdinalIgnoreCase) || name.StartsWith("OTEL_", StringComparison.OrdinalIgnoreCase)
                    || name is "DATABASE_URL" or "ENVIRONMENT") start.Environment.Remove(name);
            start.Environment["TECHSTACK_LISTEN_ADDR"] = origin.Authority;
            start.Environment["TECHSTACK_GRPC_ADDR"] = "127.0.0.1:" + WalletProbePort();
            start.Environment["TECHSTACK_DATA_DIR"] = dataDirectory;
            start.Environment["TECHSTACK_PB_DATA_DIR"] = Path.Combine(dataDirectory, "pb_data");
            start.Environment["TECHSTACK_RUNTIME_LOG_PATH"] = Path.Combine(dataDirectory, "runtime-logs.jsonl");
            start.Environment["TECHSTACK_PUBLIC_ORIGIN"] = origin.GetLeftPart(UriPartial.Authority);
            start.Environment["TECHSTACK_CORS_ORIGINS"] = origin.GetLeftPart(UriPartial.Authority);
            start.Environment["TECHSTACK_ENV"] = "local";
            start.Environment["KOMBIFY_EDITION"] = "selfhost-oss";
            start.Environment["TECHSTACK_EMBEDDED_POSTGRES"] = "1";
            start.Environment["TECHSTACK_EMBEDDED_POSTGRES_PORT"] = postgresPort.ToString();
            start.Environment["TECHSTACK_EMBEDDED_POSTGRES_BUNDLE_DIR"] = ownBundle;
            start.Environment["TECHSTACK_V2_SESSION_SECRET"] = signing;
            start.Environment["TECHSTACK_ENCRYPTION_KEY"] = encryption;
            start.Environment["TECHSTACK_V2_SESSION_AUDIENCE"] = "techstack-local";
            start.Environment["TECHSTACK_V2_DEFAULT_TENANT_ID"] = "default";
            start.Environment["TECHSTACK_LOCAL_DEVICE_TOKEN"] = device;
            start.Environment["TECHSTACK_CLIENT_STATE_DIR"] = state;
            start.Environment["TECHSTACK_ALLOW_UNSIGNED_WORKER_TOKEN"] = "1";
            runtime.Start(start, _ => { });
            using var readiness = new HttpClient(new HttpClientHandler { AllowAutoRedirect = false, UseCookies = false }) { Timeout = TimeSpan.FromSeconds(2) };
            var deadline = DateTime.UtcNow.AddSeconds(120);
            while (DateTime.UtcNow < deadline)
            {
                if (runtime.HasExited) throw new InvalidOperationException("Isolated Wallet runtime stopped at startup.");
                try
                {
                    using var response = await readiness.GetAsync(new Uri(origin, "/api/v1/info"));
                    if (response.IsSuccessStatusCode)
                    {
                        using var info = System.Text.Json.JsonDocument.Parse(await response.Content.ReadAsStringAsync());
                        var data = info.RootElement.GetProperty("data");
                        if (data.GetProperty("environment").GetString() != "local" || data.GetProperty("database_backend").GetString() != "postgres"
                            || data.GetProperty("edition").GetString() != "selfhost-oss") throw new InvalidOperationException("Wallet probe runtime posture does not match the isolated local PostgreSQL edition.");
                        if (expectedRevision is not null)
                        {
                            if (data.GetProperty("revision").GetString() != expectedRevision)
                                throw new InvalidOperationException("Wallet runtime source does not match the committed probe.");
                        }
                        return;
                    }
                }
                catch (HttpRequestException) { }
                catch (TaskCanceledException) { }
                await Task.Delay(500);
            }
            throw new InvalidOperationException("Isolated Wallet runtime did not become ready.");
        }
        await Start();
        using var transport = new WalletProbeTransport();
        using var http = new HttpClient(transport) { Timeout = TimeSpan.FromSeconds(30) };
        async Task<LocalWalletSession> Authenticate()
        {
            var cookies = await LocalDeviceSessionProof.AuthenticateAsync(http, origin, device);
            string Cookie(string name) => cookies.Single(value => value.StartsWith(name + "=", StringComparison.Ordinal)).Split(';')[0][(name.Length + 1)..];
            var session = new LocalWalletSession(Cookie("techstack_session"), Cookie("techstack_session_origin"), "");
            using var request = new HttpRequestMessage(HttpMethod.Get, new Uri(origin, "/api/v1/csrf"));
            request.Headers.TryAddWithoutValidation("Cookie", session.CookieHeader(""));
            using var response = await http.SendAsync(request);
            return session with { Csrf = response.Headers.GetValues("X-CSRF-Token").Single() };
        }
        var current = await Authenticate();
        async Task<System.Text.Json.JsonDocument> Api(HttpMethod method, string path, object? body = null)
        {
            using var request = new HttpRequestMessage(method, new Uri(origin, path));
            request.Headers.TryAddWithoutValidation("Cookie", current.CookieHeader(current.Csrf));
            request.Headers.TryAddWithoutValidation("X-CSRF-Token", current.Csrf);
            if (body is not null) request.Content = new StringContent(System.Text.Json.JsonSerializer.Serialize(body), System.Text.Encoding.UTF8, "application/json");
            using var response = await http.SendAsync(request);
            if (!response.IsSuccessStatusCode) throw new InvalidOperationException("Wallet probe API denied " + method + " " + path + " (" + (int)response.StatusCode + ").");
            return System.Text.Json.JsonDocument.Parse(await response.Content.ReadAsStringAsync());
        }
        var secret = "synthetic-wallet-" + Guid.NewGuid().ToString("N");
        using var created = await Api(HttpMethod.Post, "/api/v1/wallet", new { name = "Native Wallet boundary probe", kind = "password", secret, revealable = true });
        var assignedItem = created.RootElement.GetProperty("data").GetProperty("id").GetString()!;
        // Fixture setup uses a canonical-style opaque ID, not a generator
        // coverage claim. Owner, tenant and encrypted values remain unchanged.
        var item = assignedItem + ":pocketid:owner";
        StopWalletProbe(runtime, dataDirectory);
        await WalletProbeClosed(origin);
        await SeedOpaqueWalletProbeId(dataDirectory, assignedItem, item, "device:" + LocalWalletRevealProof.Hash(device));
        await Start();
        current = await Authenticate();
        Task<LocalWalletResult?> Reveal(string reason, Func<LocalWalletChallenge, CancellationToken, Task<bool>> confirmation,
            Func<CancellationToken, Task<LocalWalletSession?>>? sessionCheck = null) => LocalWalletRevealProof.RevealAsync(http, origin, device,
                current, item, reason, confirmation, sessionCheck ?? (_ => Task.FromResult<LocalWalletSession?>(current)));
        if (await Reveal("cancelled-probe", (_, _) => Task.FromResult(false)) is not null) throw new InvalidOperationException("Native cancellation disclosed a value.");
        async Task Denied(Func<Task<LocalWalletResult?>> operation)
        {
            try { await operation(); }
            catch (InvalidOperationException) { return; }
            throw new InvalidOperationException("Wallet boundary allowed an invalid disclosure.");
        }
        await Denied(() => Reveal("stale-document-probe", (_, _) => Task.FromResult(true), _ => Task.FromResult<LocalWalletSession?>(null)));
        await Denied(() => Reveal("changed-cookie-probe", (_, _) => Task.FromResult(true), _ => Task.FromResult<LocalWalletSession?>(current with { Origin = "changed" })));
        transport.MalformedChallenge = true;
        await Denied(() => Reveal("malformed-reply-probe", (_, _) => throw new InvalidOperationException("Malformed challenge reached native confirmation.")));
        transport.MalformedChallenge = false;
        await Denied(() => Reveal("rotated-during-dialog-probe", async (_, _) =>
        {
            using var changed = await Api(HttpMethod.Patch, "/api/v1/wallet/" + item, new { name = "Renamed while confirming", secret });
            return true;
        }));
        transport.TamperResult = true;
        await Denied(() => Reveal("tampered-result-probe", (_, _) => Task.FromResult(true)));
        transport.TamperResult = false;
        var revealed = await Reveal("windows-local-proof-probe", (_, _) => Task.FromResult(true));
        if (revealed?.Id != item || revealed.Secret != secret) throw new InvalidOperationException("Native authenticated reveal returned a different value.");
        using (var replay = await transport.Replay(http))
            if (replay.StatusCode != System.Net.HttpStatusCode.Forbidden) throw new InvalidOperationException("Wallet proof replay was accepted.");
        async Task AssertAudit()
        {
            using var activity = await Api(HttpMethod.Get, "/api/v1/activity?limit=100");
            var events = activity.RootElement.GetProperty("data").GetProperty("items").EnumerateArray().ToArray();
            bool Matches(System.Text.Json.JsonElement entry, string reason) => entry.GetProperty("action").GetString() == "wallet_reveal"
                && entry.GetProperty("details").GetProperty("resource_id").GetString() == item
                && entry.GetProperty("details").GetProperty("reason").GetString() == reason;
            if (!events.Any(entry => Matches(entry, "windows-local-proof-probe")) || events.Any(entry => Matches(entry, "cancelled-probe")))
                throw new InvalidOperationException("Wallet disclosure audit does not match confirmed/cancelled effects.");
        }
        await AssertAudit();
        StopWalletProbe(runtime, dataDirectory);
        await WalletProbeClosed(origin);
        await Start();
        current = await Authenticate();
        using var retained = await Api(HttpMethod.Get, "/api/v1/wallet");
        if (!retained.RootElement.GetProperty("data").GetProperty("items").EnumerateArray().Any(entry => entry.GetProperty("id").GetString() == item))
            throw new InvalidOperationException("Device-owned Wallet data was lost across restart.");
        await AssertAudit();
        var afterRestart = await Reveal("windows-local-restart-probe", (_, _) => Task.FromResult(true));
        if (afterRestart?.Secret != secret) throw new InvalidOperationException("Retained Wallet custody failed after restart.");
        using var runtimeBytes = File.OpenRead(executable);
        Console.WriteLine("PASS WindowsLocalWalletDeviceRevealBoundary production native transport/API/PostgreSQL/restart; runtime_sha256="
            + Convert.ToHexString(System.Security.Cryptography.SHA256.HashData(runtimeBytes)).ToLowerInvariant() + "; source=" + (expectedRevision ?? "uncommitted"));
    }
    finally
    {
        var cleanupErrors = new List<Exception>();
        try { StopWalletProbe(runtime, dataDirectory); await WalletProbeClosed(origin); } catch (Exception error) { cleanupErrors.Add(error); }
        foreach (var target in credentialTargets)
            try { WindowsCredentialStore.Delete(target); } catch (Exception error) { cleanupErrors.Add(error); }
        try { Environment.SetEnvironmentVariable("TECHSTACK_CLIENT_STATE_DIR", previousState); } catch (Exception error) { cleanupErrors.Add(error); }
        var fullState = Path.GetFullPath(state);
        if (cleanupErrors.Count == 0 && fullState.StartsWith(Path.GetFullPath(root) + Path.DirectorySeparatorChar, StringComparison.OrdinalIgnoreCase)
            && Path.GetFileName(fullState).StartsWith("wallet-live-probe-", StringComparison.Ordinal) && Directory.Exists(fullState)) Directory.Delete(fullState, true);
        if (cleanupErrors.Count != 0) throw new AggregateException("Wallet probe cleanup failed; isolated state was preserved.", cleanupErrors);
    }
}

static int WalletProbePort()
{
    var listener = new System.Net.Sockets.TcpListener(System.Net.IPAddress.Loopback, 0);
    listener.Start();
    var port = ((System.Net.IPEndPoint)listener.LocalEndpoint).Port;
    listener.Stop();
    return port;
}

static async Task WalletProbeClosed(Uri origin)
{
    var deadline = DateTime.UtcNow.AddSeconds(5);
    while (DateTime.UtcNow < deadline)
    {
        using var socket = new System.Net.Sockets.TcpClient();
        try { await socket.ConnectAsync(System.Net.IPAddress.Loopback, origin.Port); }
        catch (System.Net.Sockets.SocketException) { return; }
        await Task.Delay(100);
    }
    throw new InvalidOperationException("Owned Wallet HTTP listener did not close; fixture preserved.");
}

static void StopWalletProbe(SupervisedProcess runtime, string dataDirectory)
{
    var binaries = Path.Combine(dataDirectory, "postgres", "runtime");
    var pidPath = Path.Combine(dataDirectory, "postgres", "data", "postmaster.pid");
    using var postgres = OwnedWalletProbePostgres(dataDirectory);
    runtime.Stop();
    if (postgres is null || postgres.HasExited) return;
    if (!File.Exists(pidPath) || File.ReadLines(pidPath).FirstOrDefault() != postgres.Id.ToString())
        throw new InvalidOperationException("Owned PostgreSQL PID changed; fixture preserved.");
    var pgctl = Directory.EnumerateFiles(binaries, "pg_ctl.exe", SearchOption.AllDirectories).FirstOrDefault();
    if (pgctl is null) throw new InvalidOperationException("Owned PostgreSQL stop tool missing; fixture preserved.");
    var start = new System.Diagnostics.ProcessStartInfo(pgctl) { CreateNoWindow = true, UseShellExecute = false,
        RedirectStandardOutput = true, RedirectStandardError = true };
    foreach (var argument in new[] { "stop", "-D", Path.Combine(dataDirectory, "postgres", "data"), "-m", "fast", "-w", "-t", "10" }) start.ArgumentList.Add(argument);
    using var stop = System.Diagnostics.Process.Start(start);
    if (stop is null) throw new InvalidOperationException("Owned PostgreSQL stop did not start; fixture preserved.");
    if (!stop.WaitForExit(12000)) { stop.Kill(true); stop.WaitForExit(); throw new InvalidOperationException("Owned PostgreSQL stop timed out; fixture preserved."); }
    if (stop.ExitCode != 0 || !postgres.WaitForExit(3000)) throw new InvalidOperationException("Owned PostgreSQL did not stop; fixture preserved.");
}

static System.Diagnostics.Process? OwnedWalletProbePostgres(string dataDirectory)
{
    var binaries = Path.Combine(dataDirectory, "postgres", "runtime");
    var pidPath = Path.Combine(dataDirectory, "postgres", "data", "postmaster.pid");
    if (!File.Exists(pidPath)) return null;
    if (!int.TryParse(File.ReadLines(pidPath).FirstOrDefault(), out var pid)) throw new InvalidOperationException("Invalid owned PostgreSQL PID; fixture preserved.");
    System.Diagnostics.Process postgres;
    try { postgres = System.Diagnostics.Process.GetProcessById(pid); }
    catch (ArgumentException) { return null; }
    try
    {
        if (!postgres.HasExited)
        {
            var image = postgres.MainModule?.FileName;
            if (image is null || !Path.GetFullPath(image).StartsWith(Path.GetFullPath(binaries) + Path.DirectorySeparatorChar, StringComparison.OrdinalIgnoreCase)
                || Path.GetFileName(image) != "postgres.exe") throw new InvalidOperationException("PostgreSQL ownership could not be verified; fixture preserved.");
        }
        return postgres;
    }
    catch { postgres.Dispose(); throw; }
}

static async Task SeedOpaqueWalletProbeId(string dataDirectory, string assignedId, string opaqueId, string owner)
{
    if (!Guid.TryParse(assignedId, out _) || opaqueId != assignedId + ":pocketid:owner"
        || owner.Length != 71 || !owner.StartsWith("device:", StringComparison.Ordinal)
        || !owner[7..].All(c => c is >= '0' and <= '9' or >= 'a' and <= 'f')) throw new InvalidOperationException("Unsafe opaque Wallet fixture identity.");
    using var postgres = OwnedWalletProbePostgres(dataDirectory);
    if (postgres is not null && !postgres.HasExited) throw new InvalidOperationException("Owned Wallet fixture PostgreSQL must be stopped before seed.");
    var binaries = Path.Combine(dataDirectory, "postgres", "runtime");
    var executable = Directory.EnumerateFiles(binaries, "postgres.exe", SearchOption.AllDirectories).FirstOrDefault()
        ?? throw new InvalidOperationException("Owned Wallet fixture database helper unavailable.");
    var start = new System.Diagnostics.ProcessStartInfo(executable) { WorkingDirectory = dataDirectory, CreateNoWindow = true, UseShellExecute = false,
        RedirectStandardInput = true, RedirectStandardOutput = true, RedirectStandardError = true };
    foreach (var name in start.Environment.Keys.Where(name => name.StartsWith("PG", StringComparison.OrdinalIgnoreCase)).ToArray()) start.Environment.Remove(name);
    // Existing PostgreSQL single-user primitive; first flag and final database
    // are prescribed by PostgreSQL. No client dependency or catalog changes.
    foreach (var argument in new[] { "--single", "-D", Path.Combine(dataDirectory, "postgres", "data"), "techstack" }) start.ArgumentList.Add(argument);
    using var seed = System.Diagnostics.Process.Start(start) ?? throw new InvalidOperationException("Wallet fixture seed did not start.");
    var stdout = seed.StandardOutput.ReadToEndAsync();
    var stderr = seed.StandardError.ReadToEndAsync();
    await seed.StandardInput.WriteLineAsync("BEGIN; SELECT set_config('app.tenant_id','default',true); UPDATE wallet_items SET id='" + opaqueId + "', metadata_json=jsonb_set(metadata_json, '{id}', to_jsonb('"
        + opaqueId + "'::text), true) WHERE id='" + assignedId + "' AND tenant_id='default' AND metadata_json->>'owner_id'='" + owner + "'; COMMIT;");
    seed.StandardInput.Close();
    using var timeout = new CancellationTokenSource(TimeSpan.FromSeconds(10));
    try { await seed.WaitForExitAsync(timeout.Token); }
    catch (OperationCanceledException) { seed.Kill(true); await seed.WaitForExitAsync(); throw new InvalidOperationException("Owned Wallet fixture seed timed out."); }
    await Task.WhenAll(stdout, stderr);
    if (seed.ExitCode != 0 || stderr.Result.Contains("ERROR:", StringComparison.Ordinal)) throw new InvalidOperationException("Owned Wallet fixture seed failed.");
}

sealed class WalletProbeTransport : DelegatingHandler
{
    internal bool MalformedChallenge, TamperResult;
    private Uri? replayUri;
    private string? replayBody;
    private string? replayCookies;
    private string? replayCsrf;
    internal WalletProbeTransport() : base(new HttpClientHandler { AllowAutoRedirect = false, UseCookies = false }) { }
    protected override async Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken ct)
    {
        if (MalformedChallenge && request.RequestUri!.AbsolutePath.EndsWith("/device-reauth/challenge", StringComparison.Ordinal))
            return new HttpResponseMessage(System.Net.HttpStatusCode.OK) { Content = new StringContent("{\"data\":{}}") };
        if (request.RequestUri!.AbsolutePath.EndsWith("/reveal", StringComparison.Ordinal))
        {
            replayUri = request.RequestUri;
            replayBody = await request.Content!.ReadAsStringAsync(ct);
            replayCookies = request.Headers.GetValues("Cookie").Single();
            replayCsrf = request.Headers.GetValues("X-CSRF-Token").Single();
        }
        var response = await base.SendAsync(request, ct);
        if (TamperResult && response.IsSuccessStatusCode && request.RequestUri.AbsolutePath.EndsWith("/reveal", StringComparison.Ordinal))
        { response.Content = new StringContent("{\"data\":{\"payload\":\"e30\",\"proof\":\"" + new string('0', 64) + "\"}}"); }
        return response;
    }
    internal async Task<HttpResponseMessage> Replay(HttpClient http)
    {
        using var request = new HttpRequestMessage(HttpMethod.Post, replayUri) { Content = new StringContent(replayBody!, System.Text.Encoding.UTF8, "application/json") };
        request.Headers.TryAddWithoutValidation("Cookie", replayCookies);
        request.Headers.TryAddWithoutValidation("X-CSRF-Token", replayCsrf);
        return await http.SendAsync(request);
    }
}
