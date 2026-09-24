using System.Diagnostics;
using System.Net;
using System.Net.NetworkInformation;
using System.Reflection;
using System.Security.Cryptography;
using System.Text.Json;
using System.Text.Json.Serialization;
using Microsoft.Win32;

namespace Kombify.TechStack.Client;

// Shell half of the signed update consumer (NATIVE-CLIENT-PLATFORM-STANDARD
// section 7). The installed runtime (`techstack client-update`) verifies the
// channel manifest against the embedded key and stages the installer. This
// class applies a staged installer at start, before the runtime is spawned,
// verifies the result on the next start and rolls back when the updated
// runtime does not become healthy. Only the per-machine installation (the
// shipped Setup.exe/MSI) updates itself; update problems never block a start.
internal static class ClientUpdater
{
    private const string BundleUpgradeCode = "{E832F922-4AEC-4C41-A7B6-9546279CD913}";
    private const string SetupFileName = "kombify-Techstack-Setup.exe";
    private const string PhaseApplying = "applying";
    private const string PhaseRollingBack = "rolling-back";
    private static readonly JsonSerializerOptions JsonOptions = new(JsonSerializerDefaults.Web) { WriteIndented = true };

    internal static string UpdatesDirectory() => Path.Combine(ClientConfig.StateDirectory(), "updates");

    private static string PendingPath() => Path.Combine(UpdatesDirectory(), "pending.json");

    private static string SnapshotDirectory() => Path.Combine(UpdatesDirectory(), "snapshot");

    internal static string CurrentVersion()
    {
        var informational = Assembly.GetEntryAssembly()?
            .GetCustomAttribute<AssemblyInformationalVersionAttribute>()?.InformationalVersion ?? "";
        return informational.Split('+')[0].Trim();
    }

    internal static bool IsMachineInstallation()
    {
        var expected = Path.GetFullPath(Path.Combine(
            Environment.GetFolderPath(Environment.SpecialFolder.ProgramFiles), "kombify", "techstack"))
            .TrimEnd(Path.DirectorySeparatorChar);
        var actual = Path.GetFullPath(AppContext.BaseDirectory).TrimEnd(Path.DirectorySeparatorChar);
        return actual.Equals(expected, StringComparison.OrdinalIgnoreCase);
    }

    // Runs before the window exists. Returns true when an installer was
    // launched and this process must exit so its files can be replaced.
    internal static bool TryApplyStagedUpdate(ClientConfig config)
    {
        try
        {
            FinishRollback(config);
            if (!config.AutoUpdate || !IsMachineInstallation() || File.Exists(PendingPath()))
            {
                return false;
            }

            var current = CurrentVersion();
            var staged = NewestStagedUpdate(current);
            if (staged is null)
            {
                return false;
            }
            if (LocalRuntimePortInUse(config))
            {
                Log(config, $"client update {staged.Version} is staged; a local runtime is still running, so it applies at the next start");
                return false;
            }

            var stageDir = Path.GetDirectoryName(staged.DescriptorPath)!;
            var installer = Path.Combine(stageDir, SetupFileName);
            CopyVerified(staged.PackagePath, installer, staged.PackageSha256, staged.PackageSize);
            ReplaceDirectory(config.RuntimeDataDir, SnapshotDirectory());
            var pending = new PendingUpdate
            {
                From = current,
                To = staged.Version,
                Installer = installer,
                RollbackInstaller = CaptureRollbackInstaller(current),
                Phase = PhaseApplying,
                StartedAt = DateTimeOffset.UtcNow,
            };
            WritePending(pending);
            Log(config, $"applying client update {current} -> {staged.Version}; rollback installer {(pending.RollbackInstaller.Length > 0 ? "kept" : "unavailable")}");
            LaunchAndRestart(("KOMBIFY_UPDATE_INSTALLER", installer),
                "start \"\" /wait \"%KOMBIFY_UPDATE_INSTALLER%\" /passive /norestart");
            return true;
        }
        catch (Exception ex)
        {
            Log(config, $"client update was not applied: {ex.Message}");
            return false;
        }
    }

    // Runs once the local runtime was started (or failed to start) on a start
    // that follows an applied update. Returns true when a rollback installer
    // was launched and this process must exit.
    internal static bool CompletePendingUpdate(ClientConfig config, bool runtimeHealthy, Action stopRuntime)
    {
        var pending = ReadPending();
        if (pending is null)
        {
            return false;
        }

        try
        {
            var current = CurrentVersion();
            if (SameVersion(current, pending.To) && pending.Phase == PhaseApplying)
            {
                if (runtimeHealthy)
                {
                    Log(config, $"client update applied: {pending.From} -> {pending.To}");
                    DeleteDirectory(SnapshotDirectory());
                    DeleteDirectory(Path.Combine(UpdatesDirectory(), "rollback"));
                    DeleteAppliedStages(current);
                    File.Delete(PendingPath());
                    return false;
                }
                if (File.Exists(pending.Installer) && File.Exists(pending.RollbackInstaller))
                {
                    stopRuntime();
                    WritePending(pending with { Phase = PhaseRollingBack });
                    Log(config, $"client update {pending.To} failed its health check; rolling back to {pending.From}");
                    LaunchAndRestart(
                        ("KOMBIFY_UPDATE_INSTALLER", pending.Installer),
                        "start \"\" /wait \"%KOMBIFY_UPDATE_INSTALLER%\" /uninstall /passive /norestart & " +
                        "start \"\" /wait \"%KOMBIFY_UPDATE_ROLLBACK%\" /passive /norestart",
                        ("KOMBIFY_UPDATE_ROLLBACK", pending.RollbackInstaller));
                    return true;
                }
                Log(config, $"client update {pending.To} failed its health check and no rollback installer is available; the pre-update data snapshot is kept at {SnapshotDirectory()}");
                MarkFailed(pending.To);
                File.Delete(PendingPath());
                return false;
            }

            Log(config, SameVersion(current, pending.From)
                ? $"client update {pending.To} did not install; staying on {pending.From}"
                : $"client update {pending.From} -> {pending.To} ended on {current}");
            MarkFailed(pending.To);
            File.Delete(PendingPath());
            return false;
        }
        catch (Exception ex)
        {
            Log(config, $"client update verification failed: {ex.Message}");
            return false;
        }
    }

    // Stages a newer signed installer through the runtime binary. Never throws.
    internal static async Task CheckAndStageAsync(ClientConfig config, string runtimeExe)
    {
        if (!config.AutoUpdate || !IsMachineInstallation() ||
            string.IsNullOrWhiteSpace(config.UpdateManifestUrl) || !File.Exists(runtimeExe))
        {
            return;
        }

        try
        {
            var start = new ProcessStartInfo(runtimeExe)
            {
                UseShellExecute = false,
                CreateNoWindow = true,
                RedirectStandardOutput = true,
                RedirectStandardError = true,
            };
            foreach (var argument in new[]
            {
                "client-update",
                "--manifest-url", config.UpdateManifestUrl,
                "--channel", config.UpdateChannel,
                "--staging-dir", UpdatesDirectory(),
            })
            {
                start.ArgumentList.Add(argument);
            }
            if (!string.IsNullOrWhiteSpace(config.UpdateMinSupportedVersion))
            {
                start.ArgumentList.Add("--min-supported-version");
                start.ArgumentList.Add(config.UpdateMinSupportedVersion);
            }

            using var process = Process.Start(start);
            if (process is null)
            {
                return;
            }
            using var budget = new CancellationTokenSource(TimeSpan.FromMinutes(15));
            var stdout = process.StandardOutput.ReadToEndAsync(budget.Token);
            var stderr = process.StandardError.ReadToEndAsync(budget.Token);
            await process.WaitForExitAsync(budget.Token);
            Log(config, $"client update check: {(await stdout).Trim()} {(await stderr).Trim()}".Trim());
        }
        catch (Exception ex)
        {
            Log(config, $"client update check failed: {ex.Message}");
        }
    }

    // A rolled-back installation restores the pre-update runtime data before
    // its runtime starts, because the newer runtime may have migrated it.
    private static void FinishRollback(ClientConfig config)
    {
        var pending = ReadPending();
        if (pending is null || pending.Phase != PhaseRollingBack || !SameVersion(CurrentVersion(), pending.From))
        {
            return;
        }
        if (EmbeddedPostgresRunning(config))
        {
            Log(config, "client update rollback waits: the embedded PostgreSQL of the updated runtime is still running");
            return;
        }
        var snapshot = SnapshotDirectory();
        if (Directory.Exists(snapshot))
        {
            RestoreDirectory(snapshot, config.RuntimeDataDir);
            DeleteDirectory(snapshot);
        }
        MarkFailed(pending.To);
        File.Delete(PendingPath());
        Log(config, $"client update rolled back to {pending.From}; runtime data restored from the pre-update snapshot");
    }

    private static StagedUpdate? NewestStagedUpdate(string current)
    {
        var root = UpdatesDirectory();
        if (!Directory.Exists(root))
        {
            return null;
        }

        StagedUpdate? newest = null;
        foreach (var descriptorPath in Directory.EnumerateFiles(root, "stage.json", SearchOption.AllDirectories))
        {
            try
            {
                var staged = JsonSerializer.Deserialize<StagedUpdate>(File.ReadAllText(descriptorPath));
                if (staged is null || File.Exists(Path.Combine(Path.GetDirectoryName(descriptorPath)!, "failed")) ||
                    CompareVersions(staged.Version, current) is not > 0 ||
                    (newest is not null && CompareVersions(staged.Version, newest.Version) is not > 0))
                {
                    continue;
                }
                newest = staged with { DescriptorPath = descriptorPath };
            }
            catch (JsonException)
            {
                // A malformed descriptor is never applied.
            }
        }
        return newest;
    }

    private static void CopyVerified(string source, string destination, string expectedSha256, long expectedSize)
    {
        // The source stays open without write sharing while it is hashed and
        // copied, and the copy is hashed again before it is launched.
        using (var input = new FileStream(source, FileMode.Open, FileAccess.Read, FileShare.Read))
        using (var output = new FileStream(destination, FileMode.Create, FileAccess.Write, FileShare.None))
        {
            input.CopyTo(output);
        }
        using var copy = new FileStream(destination, FileMode.Open, FileAccess.Read, FileShare.Read);
        var digest = Convert.ToHexString(SHA256.HashData(copy)).ToLowerInvariant();
        if (copy.Length != expectedSize || !digest.Equals(expectedSha256, StringComparison.OrdinalIgnoreCase))
        {
            copy.Dispose();
            File.Delete(destination);
            throw new InvalidOperationException("the staged installer no longer matches its verified digest");
        }
    }

    // Burn keeps the installed bundle in its package cache; a copy of that
    // exact installer is what a rollback reinstalls.
    private static string CaptureRollbackInstaller(string current)
    {
        foreach (var view in new[] { RegistryView.Registry32, RegistryView.Registry64 })
        {
            using var hive = RegistryKey.OpenBaseKey(RegistryHive.LocalMachine, view);
            using var uninstall = hive.OpenSubKey(@"SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall");
            if (uninstall is null)
            {
                continue;
            }
            foreach (var name in uninstall.GetSubKeyNames())
            {
                using var entry = uninstall.OpenSubKey(name);
                var upgradeCodes = entry?.GetValue("BundleUpgradeCode") switch
                {
                    string[] codes => codes,
                    string code => new[] { code },
                    _ => Array.Empty<string>(),
                };
                if (!upgradeCodes.Any(code => code.Equals(BundleUpgradeCode, StringComparison.OrdinalIgnoreCase)) ||
                    !SameVersion(entry?.GetValue("DisplayVersion") as string ?? "", current) ||
                    entry?.GetValue("BundleCachePath") is not string cached || !File.Exists(cached))
                {
                    continue;
                }
                var rollbackDir = Path.Combine(UpdatesDirectory(), "rollback", current);
                Directory.CreateDirectory(rollbackDir);
                var rollback = Path.Combine(rollbackDir, SetupFileName);
                File.Copy(cached, rollback, overwrite: true);
                return rollback;
            }
        }
        return "";
    }

    private static bool LocalRuntimePortInUse(ClientConfig config)
    {
        var port = config.LocalUiUri()?.Port ?? 5260;
        return IPGlobalProperties.GetIPGlobalProperties().GetActiveTcpListeners()
            .Any(endpoint => endpoint.Port == port &&
                (IPAddress.IsLoopback(endpoint.Address) || endpoint.Address.Equals(IPAddress.Any)));
    }

    private static bool EmbeddedPostgresRunning(ClientConfig config)
    {
        var pidFile = Path.Combine(config.RuntimeDataDir, "postgres", "data", "postmaster.pid");
        if (!File.Exists(pidFile) || !int.TryParse(File.ReadLines(pidFile).FirstOrDefault(), out var pid))
        {
            return false;
        }
        try
        {
            using var process = Process.GetProcessById(pid);
            return process.ProcessName.Equals("postgres", StringComparison.OrdinalIgnoreCase);
        }
        catch (ArgumentException)
        {
            return false;
        }
    }

    // The installer replaces this executable, so a detached command waits until
    // this process has exited, runs the installer(s) and then starts the (new)
    // client, which inherits this environment. Paths travel as environment
    // variables so that no path character is parsed as a command.
    private static void LaunchAndRestart((string Name, string Value) first, string installerCommands,
        params (string Name, string Value)[] more)
    {
        var start = new ProcessStartInfo("cmd.exe")
        {
            UseShellExecute = false,
            CreateNoWindow = true,
            Arguments = "/d /s /c \"ping -n 4 127.0.0.1 >nul & " + installerCommands +
                " & start \"\" \"%KOMBIFY_UPDATE_CLIENT%\"\"",
        };
        start.Environment["KOMBIFY_UPDATE_CLIENT"] = Path.Combine(AppContext.BaseDirectory, "kombify-techstack-client.exe");
        start.Environment[first.Name] = first.Value;
        foreach (var (name, value) in more)
        {
            start.Environment[name] = value;
        }
        Process.Start(start)?.Dispose();
    }

    private static void DeleteAppliedStages(string current)
    {
        foreach (var directory in Directory.EnumerateDirectories(UpdatesDirectory()))
        {
            if (CompareVersions(Path.GetFileName(directory), current) is <= 0 &&
                !File.Exists(Path.Combine(directory, "failed")))
            {
                Directory.Delete(directory, recursive: true);
            }
        }
    }

    private static void MarkFailed(string version)
    {
        if (string.IsNullOrWhiteSpace(version) || version.IndexOfAny(Path.GetInvalidFileNameChars()) >= 0)
        {
            return;
        }
        var directory = Path.Combine(UpdatesDirectory(), version);
        Directory.CreateDirectory(directory);
        File.WriteAllText(Path.Combine(directory, "failed"), DateTimeOffset.UtcNow.ToString("O"));
    }

    private static PendingUpdate? ReadPending()
    {
        try
        {
            return File.Exists(PendingPath())
                ? JsonSerializer.Deserialize<PendingUpdate>(File.ReadAllText(PendingPath()), JsonOptions)
                : null;
        }
        catch (JsonException)
        {
            return null;
        }
    }

    private static void WritePending(PendingUpdate pending)
    {
        Directory.CreateDirectory(UpdatesDirectory());
        File.WriteAllText(PendingPath(), JsonSerializer.Serialize(pending, JsonOptions));
    }

    private static void ReplaceDirectory(string source, string destination)
    {
        DeleteDirectory(destination);
        if (Directory.Exists(source))
        {
            CopyDirectory(source, destination);
        }
    }

    private static void RestoreDirectory(string snapshot, string target)
    {
        Directory.CreateDirectory(target);
        foreach (var entry in Directory.EnumerateFileSystemEntries(target))
        {
            if (Directory.Exists(entry))
            {
                Directory.Delete(entry, recursive: true);
            }
            else if (!entry.EndsWith(".log", StringComparison.OrdinalIgnoreCase))
            {
                File.Delete(entry);
            }
        }
        CopyDirectory(snapshot, target);
    }

    // Logs stay in place so a failed update remains diagnosable.
    private static void CopyDirectory(string source, string destination)
    {
        Directory.CreateDirectory(destination);
        foreach (var file in Directory.EnumerateFiles(source))
        {
            if (!file.EndsWith(".log", StringComparison.OrdinalIgnoreCase))
            {
                File.Copy(file, Path.Combine(destination, Path.GetFileName(file)), overwrite: true);
            }
        }
        foreach (var directory in Directory.EnumerateDirectories(source))
        {
            // Extracted PostgreSQL binaries are recreated from the installed
            // bundle at start; they are not data.
            var extractedBinaries = Path.GetFileName(directory).Equals("runtime", StringComparison.OrdinalIgnoreCase) &&
                Path.GetFileName(source).Equals("postgres", StringComparison.OrdinalIgnoreCase);
            if (!extractedBinaries && (File.GetAttributes(directory) & FileAttributes.ReparsePoint) == 0)
            {
                CopyDirectory(directory, Path.Combine(destination, Path.GetFileName(directory)));
            }
        }
    }

    private static void DeleteDirectory(string path)
    {
        if (Directory.Exists(path))
        {
            Directory.Delete(path, recursive: true);
        }
    }

    private static bool SameVersion(string left, string right) => CompareVersions(left, right) == 0;

    // SemVer precedence for MAJOR.MINOR.PATCH with an optional pre-release;
    // null when either side is not a version.
    internal static int? CompareVersions(string left, string right)
    {
        static (long[] Core, string Pre)? Parse(string value)
        {
            var text = value.Trim().Split('+')[0];
            var dash = text.IndexOf('-');
            var core = dash < 0 ? text : text[..dash];
            var parts = core.Split('.');
            if (parts.Length != 3)
            {
                return null;
            }
            var numbers = new long[3];
            for (var i = 0; i < 3; i++)
            {
                if (!long.TryParse(parts[i], out numbers[i]) || numbers[i] < 0)
                {
                    return null;
                }
            }
            return (numbers, dash < 0 ? "" : text[(dash + 1)..]);
        }

        if (Parse(left) is not { } a || Parse(right) is not { } b)
        {
            return null;
        }
        for (var i = 0; i < 3; i++)
        {
            if (a.Core[i] != b.Core[i])
            {
                return a.Core[i].CompareTo(b.Core[i]);
            }
        }
        if (a.Pre.Length == 0 || b.Pre.Length == 0)
        {
            return (b.Pre.Length == 0 ? 0 : 1) - (a.Pre.Length == 0 ? 0 : 1);
        }
        return string.CompareOrdinal(a.Pre, b.Pre);
    }

    private static void Log(ClientConfig config, string message)
    {
        try
        {
            Directory.CreateDirectory(config.RuntimeDataDir);
        }
        catch
        {
            return;
        }
        ClientWindow.AppendRuntimeLog(config.RuntimeLogPath(), $"[{DateTimeOffset.Now:u}] {message}");
    }

    private sealed record StagedUpdate
    {
        [JsonPropertyName("version")] public string Version { get; init; } = "";
        [JsonPropertyName("channel")] public string Channel { get; init; } = "";
        [JsonPropertyName("package_path")] public string PackagePath { get; init; } = "";
        [JsonPropertyName("package_sha256")] public string PackageSha256 { get; init; } = "";
        [JsonPropertyName("package_size")] public long PackageSize { get; init; }
        [JsonIgnore] public string DescriptorPath { get; init; } = "";
    }

    private sealed record PendingUpdate
    {
        public string From { get; init; } = "";
        public string To { get; init; } = "";
        public string Installer { get; init; } = "";
        public string RollbackInstaller { get; init; } = "";
        public string Phase { get; init; } = "";
        public DateTimeOffset StartedAt { get; init; }
    }
}
