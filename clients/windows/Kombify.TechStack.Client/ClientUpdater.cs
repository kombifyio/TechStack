using System.Diagnostics;
using System.Net;
using System.Net.NetworkInformation;
using System.Reflection;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using System.Text.Json.Serialization;
using Kombify.Client.Shell;

namespace Kombify.TechStack.Client;

// Shell half of the signed update consumer (NATIVE-CLIENT-PLATFORM-STANDARD
// section 7). The installed runtime (`techstack client-update`) verifies the
// channel manifest against the embedded key and stages the installer. This
// class applies a staged installer at start, before the runtime is spawned,
// verifies the result on the next start and rolls back when the updated
// runtime does not become healthy. Only the per-machine installation (the
// shipped Setup.exe/MSI) updates itself. Update checks never block a start;
// rollback recovery must finish before a runtime can use retained data.
internal static class ClientUpdater
{
    private const string SetupFileName = DesktopEdition.InstallerFileName;
    private static string VerifierExecutable() => Path.Combine(AppContext.BaseDirectory, DesktopEdition.VerifierFileName);
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
            Environment.GetFolderPath(Environment.SpecialFolder.ProgramFiles), "kombify", DesktopEdition.InstallDirectoryName))
            .TrimEnd(Path.DirectorySeparatorChar);
        var actual = Path.GetFullPath(AppContext.BaseDirectory).TrimEnd(Path.DirectorySeparatorChar);
        return actual.Equals(expected, StringComparison.OrdinalIgnoreCase);
    }

    // Runs before the window exists. Returns true when an installer was
    // launched; a blocked recovery also prevents starting the runtime.
    internal static bool TryApplyStagedUpdate(ClientConfig config, out string? startupBlockReason)
    {
        startupBlockReason = null;
        try
        {
            if (ResumePendingRollback(config, out startupBlockReason)) return true;
            if (startupBlockReason is not null) return false;
            startupBlockReason = FinishRollback(config);
            if (startupBlockReason is not null) return false;
            if (!config.AutoUpdate || !IsMachineInstallation() || File.Exists(PendingPath()))
            {
                return false;
            }

            var current = CurrentVersion();
            var staged = NewestStagedUpdate(current, config.UpdateChannel);
            if (staged is null)
            {
                return false;
            }
            if (DesktopEdition.IsLocal && (LocalRuntimePortInUse(config) || EmbeddedPostgresRunning(config)))
            {
                Log(config, $"client update {staged.Version} is staged; the local runtime or database is still running, so it applies at the next start");
                return false;
            }

            var recovery = CaptureRollbackInstaller(current, config.UpdateChannel);
            if (recovery is null)
            {
                Log(config, $"client update {staged.Version} waits: no verified recovery installer for {current} is available");
                return false;
            }

            var stageDir = Path.Combine(UpdatesDirectory(), staged.Version);
            var installer = Path.Combine(stageDir, SetupFileName);
            var verified = VerifyStagedPackage(UpdatesDirectory(), staged.Version, config.UpdateChannel, "update", config.UpdateMinSupportedVersion);
            VerifiedUpdateHandoff.CopyVerified(verified.PackagePath, installer, verified.PackageSha256, verified.PackageSize);
            CaptureUpdateSnapshot(config);
            var pending = new PendingUpdate
            {
                From = current,
                To = staged.Version,
                Installer = installer,
                RollbackInstaller = recovery.Path,
                InstallerSha256 = verified.PackageSha256,
                InstallerSize = verified.PackageSize,
                RollbackSha256 = recovery.Sha256,
                RollbackSize = recovery.Size,
                Phase = PhaseApplying,
                StartedAt = DateTimeOffset.UtcNow,
            };
            WritePending(pending);
            Log(config, $"applying client update {current} -> {staged.Version}; rollback installer {(pending.RollbackInstaller.Length > 0 ? "kept" : "unavailable")}");
            LaunchAndRestart(config, [new InstallerHandoff(installer, verified.PackageSha256,
                verified.PackageSize, "/passive /norestart")]);
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
        var recoveryCommitted = false;
        try
        {
            var pending = ReadPending();
            if (pending is null) return false;
            // Only pre-start recovery can finish a rollback. Runtime health
            // cannot replace restoring its pre-update data.
            if (pending.Phase == PhaseRollingBack) return false;
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
                // Commit recovery before attempting an installer. A missing
                // package must not erase recovery or let the failed runtime
                // start again against data it may already have migrated.
                stopRuntime();
                WritePending(pending with { Phase = PhaseRollingBack });
                recoveryCommitted = true;
                var installers = BindRollbackInstallers(config, pending with { Phase = PhaseRollingBack }, ResolveSignedRollbackPackage);
                Log(config, $"client update {pending.To} failed its health check; rolling back to {pending.From}");
                LaunchAndRestart(config, installers);
                return true;
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
            return recoveryCommitted;
        }
    }

    // Stages a newer signed installer through the runtime binary. Never throws.
    internal static async Task CheckAndStageAsync(ClientConfig config, string runtimeExe)
    {
        runtimeExe = VerifierExecutable();
        if (!config.AutoUpdate || !IsMachineInstallation() ||
            string.IsNullOrWhiteSpace(config.UpdateManifestUrl) || !File.Exists(runtimeExe))
        {
            return;
        }

        try
        {
            // Installer authority always belongs to the installed binary,
            // never a user-configured runtime-executable override.
            var current = CurrentVersion();
            if (CaptureRollbackInstaller(current, config.UpdateChannel) is null)
            {
                var recoveryManifest = RecoveryManifestUrl(config.UpdateManifestUrl, current);
                if (recoveryManifest is null)
                    throw new InvalidOperationException("The configured update feed has no supported exact-version recovery manifest URL.");
                if (!await StagePackageAsync(config, runtimeExe, recoveryManifest,
                        Path.Combine(UpdatesDirectory(), "rollback"), current) ||
                    CaptureRollbackInstaller(current, config.UpdateChannel) is null)
                    throw new InvalidOperationException($"No signed recovery installer for installed version {current} is available; the installation was not changed.");
            }
            await StagePackageAsync(config, runtimeExe, config.UpdateManifestUrl, UpdatesDirectory(), null);
        }
        catch (Exception ex)
        {
            Log(config, $"client update check failed: {ex.Message}");
        }
    }

    private static VerifiedPackage VerifyStagedPackage(string root, string version, string channel, string purpose,
        string minimumVersion = "")
    {
        var start = new ProcessStartInfo(VerifierExecutable())
        {
            UseShellExecute = false, CreateNoWindow = true,
            RedirectStandardOutput = true, RedirectStandardError = true,
        };
        foreach (var argument in new[] { "client-update", "--verify-staged-version", version,
                     "--staging-dir", root, "--channel", channel }) start.ArgumentList.Add(argument);
        if (purpose == "recovery")
        {
            start.ArgumentList.Add("--recovery-version");
            start.ArgumentList.Add(CurrentVersion());
        }
        else if (purpose == "rollback") start.ArgumentList.Add("--verify-rollback");
        else if (!string.IsNullOrWhiteSpace(minimumVersion))
        {
            start.ArgumentList.Add("--min-supported-version");
            start.ArgumentList.Add(minimumVersion);
        }
        using var process = Process.Start(start) ?? throw new InvalidOperationException("The installed update verifier did not start.");
        var stdout = process.StandardOutput.ReadToEndAsync();
        var stderr = process.StandardError.ReadToEndAsync();
        if (!process.WaitForExit(30_000))
        {
            process.Kill(entireProcessTree: true);
            process.WaitForExit();
            throw new InvalidOperationException("The installed update verifier did not finish.");
        }
        var output = stdout.GetAwaiter().GetResult();
        var errors = stderr.GetAwaiter().GetResult();
        if (process.ExitCode != 0) throw new InvalidDataException($"The installed update verifier refused the package: {errors} {output}");
        var verified = JsonSerializer.Deserialize<VerifiedPackage>(output)
            ?? throw new InvalidDataException("The installed update verifier returned no package.");
        var expected = Path.GetFullPath(Path.Combine(root, version, "package"));
        if (verified.Status != $"verified-{purpose}" || verified.Version != version ||
            !Path.GetFullPath(verified.PackagePath).Equals(expected, StringComparison.OrdinalIgnoreCase))
            throw new InvalidDataException("The installed update verifier returned a different package.");
        return verified;
    }

    // Returning to this cursor on the next start retries signed artifact
    // acquisition; retaining a snapshot alone is not an executable rollback.
    // Dependency seams below the production caller allow isolated module probes.
    internal static bool ResumePendingRollback(ClientConfig config, out string? blocked,
        Func<ClientConfig, string, string, VerifiedPackage>? resolve = null,
        Action<InstallerHandoff[]>? launch = null)
    {
        blocked = null;
        PendingUpdate? pending;
        try { pending = ReadPending(); }
        catch (Exception ex)
        {
            Log(config, $"client rollback cursor is invalid: {ex.Message}");
            blocked = LocalRuntimeMessages.RecoveryRestoreFailed;
            return false;
        }
        if (pending is null || pending.Phase != PhaseRollingBack || SameVersion(CurrentVersion(), pending.From))
            return false;
        blocked = LocalRuntimeMessages.RecoveryInstallationPending;
        try
        {
            var installers = BindRollbackInstallers(config, pending, resolve ?? ResolveSignedRollbackPackage);
            if (launch is null) LaunchAndRestart(config, installers);
            else launch(installers);
            blocked = null;
            return true;
        }
        catch (Exception ex)
        {
            Log(config, $"client rollback remains pending: {ex.Message}; signed artifact acquisition will retry at the next start");
            return false;
        }
    }

    internal static VerifiedPackage ResolveSignedRollbackPackage(ClientConfig config, string root, string version)
    {
        try { return VerifyStagedPackage(root, version, config.UpdateChannel, "rollback"); }
        catch (Exception ex) when (ex is IOException or InvalidDataException or System.ComponentModel.Win32Exception)
        {
            var manifest = RecoveryManifestUrl(config.UpdateManifestUrl, version)
                ?? throw new InvalidOperationException("The update feed has no supported exact-version rollback manifest URL.");
            var runtime = VerifierExecutable();
            if (!StagePackageAsync(config, runtime, manifest, root, version, rollback: true)
                    .GetAwaiter().GetResult())
                throw new InvalidDataException($"No signed rollback package for exact version {version} is available.");
            return VerifyStagedPackage(root, version, config.UpdateChannel, "rollback");
        }
    }

    private static InstallerHandoff[] BindRollbackInstallers(ClientConfig config, PendingUpdate pending,
        Func<ClientConfig, string, string, VerifiedPackage> resolve)
    {
        if (CurrentVersion() != pending.To || CompareVersions(pending.From, pending.To) is not < 0)
            throw new InvalidDataException("The rollback does not belong to this installation.");
        if (!Directory.Exists(SnapshotDirectory()))
            throw new InvalidDataException("The pre-update snapshot is missing; rollback data was not changed.");
        if (DesktopEdition.IsLocal && (LocalRuntimePortInUse(config) || EmbeddedPostgresRunning(config)))
            throw new InvalidOperationException("The local runtime or PostgreSQL must stop before rollback.");
        var legacy = pending.InstallerSha256.Length == 0 && pending.RollbackSha256.Length == 0 &&
                     pending.InstallerSize == 0 && pending.RollbackSize == 0;
        var updated = resolve(config, UpdatesDirectory(), pending.To);
        if (!legacy && (!updated.PackageSha256.Equals(pending.InstallerSha256, StringComparison.OrdinalIgnoreCase) ||
            updated.PackageSize != pending.InstallerSize))
            throw new InvalidDataException("The signed update package changed since the recovery snapshot.");
        var newInstaller = Path.Combine(UpdatesDirectory(), pending.To, SetupFileName);
        VerifiedUpdateHandoff.CopyVerified(updated.PackagePath, newInstaller, updated.PackageSha256, updated.PackageSize);
        var oldInstaller = Path.Combine(UpdatesDirectory(), "rollback", pending.From, SetupFileName);
        var recovery = resolve(config, Path.Combine(UpdatesDirectory(), "rollback"), pending.From);
        if (!legacy && (!recovery.PackageSha256.Equals(pending.RollbackSha256, StringComparison.OrdinalIgnoreCase) ||
            recovery.PackageSize != pending.RollbackSize))
            throw new InvalidDataException("The signed recovery package changed since the recovery snapshot.");
        VerifiedUpdateHandoff.CopyVerified(recovery.PackagePath, oldInstaller, recovery.PackageSha256, recovery.PackageSize);
        // Legacy paths and self-declared hashes are never migration authority.
        // Publish all signer-derived bindings together before the handoff.
        WritePending(pending with { Installer = newInstaller, RollbackInstaller = oldInstaller,
            InstallerSha256 = updated.PackageSha256, InstallerSize = updated.PackageSize,
            RollbackSha256 = recovery.PackageSha256, RollbackSize = recovery.PackageSize });
        return [new InstallerHandoff(newInstaller, updated.PackageSha256, updated.PackageSize, "/uninstall /passive /norestart"),
            new InstallerHandoff(oldInstaller, recovery.PackageSha256, recovery.PackageSize, "/passive /norestart")];
    }

    internal static string? RecoveryManifestUrl(string feed, string version)
    {
        if (!DesktopEdition.IsLocal)
            return feed == DesktopEdition.UpdateManifestUrl && CompareVersions(version, version) is not null
                ? $"https://github.com/kombifyio/TechStack/releases/download/windows-cloud-v{version}/kombify-techstack-windows-cloud-beta.json"
                : null;
        if (!Uri.TryCreate(feed, UriKind.Absolute, out var url) || url.Scheme != "https" ||
            !url.Host.Equals("github.com", StringComparison.OrdinalIgnoreCase) ||
            url.UserInfo.Length != 0 || url.Query.Length != 0 || url.Fragment.Length != 0 ||
            CompareVersions(version, version) is null) return null;
        var parts = url.AbsolutePath.Split('/', StringSplitOptions.RemoveEmptyEntries);
        if (parts.Length != 6 || parts[2] != "releases" || parts[3] != "latest" || parts[4] != "download")
            return null;
        return $"https://github.com/{parts[0]}/{parts[1]}/releases/download/v{version}/{parts[5]}";
    }

    private static async Task<bool> StagePackageAsync(ClientConfig config, string runtimeExe,
        string manifestUrl, string stagingDirectory, string? recoveryVersion, bool rollback = false)
    {
        var start = new ProcessStartInfo(runtimeExe)
        {
            UseShellExecute = false, CreateNoWindow = true,
            RedirectStandardOutput = true, RedirectStandardError = true,
        };
        foreach (var argument in new[] { "client-update", "--manifest-url", manifestUrl,
                     "--channel", config.UpdateChannel, "--staging-dir", stagingDirectory })
            start.ArgumentList.Add(argument);
        if (recoveryVersion is not null)
        {
            start.ArgumentList.Add(rollback ? "--rollback-version" : "--recovery-version");
            start.ArgumentList.Add(recoveryVersion);
        }
        else if (!string.IsNullOrWhiteSpace(config.UpdateMinSupportedVersion))
        {
            start.ArgumentList.Add("--min-supported-version");
            start.ArgumentList.Add(config.UpdateMinSupportedVersion);
        }
        using var process = Process.Start(start);
        if (process is null) return false;
        using var budget = new CancellationTokenSource(TimeSpan.FromMinutes(15));
        try
        {
            var stdout = process.StandardOutput.ReadToEndAsync(budget.Token);
            var stderr = process.StandardError.ReadToEndAsync(budget.Token);
            await process.WaitForExitAsync(budget.Token).ConfigureAwait(false);
            Log(config, $"client {(rollback ? "rollback" : recoveryVersion is null ? "update" : "recovery")} check: {(await stdout.ConfigureAwait(false)).Trim()} {(await stderr.ConfigureAwait(false)).Trim()}".Trim());
            return process.ExitCode == 0;
        }
        finally
        {
            if (!process.HasExited)
            {
                process.Kill(entireProcessTree: true);
                await process.WaitForExitAsync().ConfigureAwait(false);
            }
        }
    }

    // A rolled-back installation restores the pre-update runtime data before
    // its runtime starts, because the newer runtime may have migrated it.
    private static string? FinishRollback(ClientConfig config)
    {
        try
        {
            var pending = ReadPending();
            if (pending is null || pending.Phase != PhaseRollingBack) return null;
            if (!SameVersion(CurrentVersion(), pending.From))
            {
                Log(config, "client update rollback waits: the previous installation has not been restored");
                return LocalRuntimeMessages.RecoveryInstallationPending;
            }
            if (DesktopEdition.IsLocal && (LocalRuntimePortInUse(config) || EmbeddedPostgresRunning(config)))
            {
                Log(config, "client update rollback waits: the local runtime or embedded PostgreSQL is still running");
                return LocalRuntimeMessages.RecoveryDatabaseRunning;
            }
            var snapshot = SnapshotDirectory();
            if (!Directory.Exists(snapshot))
            {
                Log(config, "client update rollback waits: the pre-update data snapshot is missing");
                return LocalRuntimeMessages.RecoverySnapshotMissing;
            }
            RestoreUpdateSnapshot(config);
            MarkFailed(pending.To);
            File.Delete(PendingPath());
            // Recovery is committed once the data is restored and its cursor
            // consumed. A cleanup failure must not block or replay that restore.
            try { DeleteDirectory(snapshot); }
            catch (Exception ex) { Log(config, $"client update rollback snapshot cleanup failed: {ex.Message}"); }
            Log(config, $"client update rolled back to {pending.From}; runtime data restored from the pre-update snapshot");
            return null;
        }
        catch (Exception ex)
        {
            Log(config, $"client update rollback could not finish: {ex.Message}");
            return LocalRuntimeMessages.RecoveryRestoreFailed;
        }
    }

    private static StagedUpdate? NewestStagedUpdate(string current, string channel)
    {
        var root = UpdatesDirectory();
        if (!Directory.Exists(root))
        {
            return null;
        }

        StagedUpdate? newest = null;
        // Recovery packages are not upgrade candidates. Only the normal
        // updater's immediate version directories belong to this channel.
        foreach (var directory in Directory.EnumerateDirectories(root))
        {
            var descriptorPath = Path.Combine(directory, "stage.json");
            if (!File.Exists(descriptorPath)) continue;
            try
            {
                var staged = JsonSerializer.Deserialize<StagedUpdate>(File.ReadAllText(descriptorPath));
                if (staged is null || Path.GetFileName(directory) != staged.Version ||
                    (File.GetAttributes(directory) & FileAttributes.ReparsePoint) != 0 ||
                    staged.Channel != channel || File.Exists(Path.Combine(directory, "failed")) ||
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

    // Both MSI and Burn recover from the signed exact installed release.
    // Burn removes its original cache during upgrade, so that cache cannot
    // authorize a retained user-state copy after the old bundle is unregistered.
    private static RecoveryInstaller? CaptureRollbackInstaller(string current, string channel)
    {
        var descriptor = Path.Combine(UpdatesDirectory(), "rollback", current, "stage.json");
        if (!File.Exists(descriptor)) return null;
        var verified = VerifyStagedPackage(Path.Combine(UpdatesDirectory(), "rollback"), current, channel, "recovery");
        var installer = Path.Combine(Path.GetDirectoryName(descriptor)!, SetupFileName);
        VerifiedUpdateHandoff.CopyVerified(verified.PackagePath, installer, verified.PackageSha256, verified.PackageSize);
        return new RecoveryInstaller(installer, verified.PackageSha256, verified.PackageSize);
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

    // The trusted inline coordinator survives this shell's exit. It hashes
    // deny-write/delete handles and holds them until the installers exit, so a
    // copied package cannot be substituted during the elevation handoff.
    private static void LaunchAndRestart(ClientConfig config, InstallerHandoff[] installers)
    {
        using var process = Process.Start(InstallerHandoffStartInfo(installers,
            Path.Combine(AppContext.BaseDirectory, "kombify-techstack-client.exe"), config.RuntimeLogPath(), Environment.ProcessId))
            ?? throw new InvalidOperationException("The installer coordinator did not start.");
    }

    internal static ProcessStartInfo InstallerHandoffStartInfo(InstallerHandoff[] installers,
        string clientPath, string logPath, int parentId)
    {
        if (installers.Length is < 1 or > 2) throw new ArgumentException("The installer handoff must contain one update or one rollback pair.");
        const string coordinator = """
            $ErrorActionPreference = 'Stop'
            $heldPackages = @()
            try {
                for ($packageIndex = 0; $packageIndex -lt [int]$env:KOMBIFY_UPDATE_COUNT; $packageIndex++) {
                    $prefix = "KOMBIFY_UPDATE_${packageIndex}_"
                    $packagePath = [Environment]::GetEnvironmentVariable($prefix + 'PATH')
                    $expectedSize = [long][Environment]::GetEnvironmentVariable($prefix + 'SIZE')
                    $expectedHash = [Environment]::GetEnvironmentVariable($prefix + 'SHA256')
                    $packageStream = [IO.FileStream]::new($packagePath, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)
                    $heldPackages += @{ Stream = $packageStream; Path = $packagePath; Arguments = [Environment]::GetEnvironmentVariable($prefix + 'ARGUMENTS') }
                    $hasher = [Security.Cryptography.SHA256]::Create()
                    try { $actualHash = [BitConverter]::ToString($hasher.ComputeHash($packageStream)).Replace('-', '') }
                    finally { $hasher.Dispose() }
                    if ($packageStream.Length -ne $expectedSize -or $actualHash -ne $expectedHash) {
                        throw 'The installer no longer matches its verified digest and size.'
                    }
                }
                if ([int]$env:KOMBIFY_UPDATE_PARENT_ID -gt 0) {
                    try { $handoffParent = [Diagnostics.Process]::GetProcessById([int]$env:KOMBIFY_UPDATE_PARENT_ID) }
                    catch [ArgumentException] { $handoffParent = $null }
                    if ($null -ne $handoffParent) { try { $handoffParent.WaitForExit() } finally { $handoffParent.Dispose() } }
                }
                foreach ($packageEntry in $heldPackages) {
                    $installerProcess = Start-Process -FilePath $packageEntry.Path -ArgumentList $packageEntry.Arguments -WindowStyle Hidden -Wait -PassThru
                    try {
                        if ($installerProcess.ExitCode -ne 0 -and $installerProcess.ExitCode -ne 3010) {
                            throw "The installer failed with exit code $($installerProcess.ExitCode). Recovery remains pending."
                        }
                    } finally { $installerProcess.Dispose() }
                }
            } catch {
                try { [IO.File]::AppendAllText($env:KOMBIFY_UPDATE_LOG, "[$([DateTimeOffset]::Now.ToString('u'))] installer handoff failed: $($_.Exception.Message)`r`n") } catch {}
                exit 1
            } finally {
                foreach ($packageEntry in $heldPackages) { $packageEntry.Stream.Dispose() }
            }
            Start-Process -FilePath $env:KOMBIFY_UPDATE_CLIENT -WindowStyle Hidden
            """;
        var start = new ProcessStartInfo(Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.System),
            "WindowsPowerShell", "v1.0", "powershell.exe"))
        {
            UseShellExecute = false,
            CreateNoWindow = true,
            Arguments = "-NoProfile -NonInteractive -WindowStyle Hidden -EncodedCommand " +
                        Convert.ToBase64String(Encoding.Unicode.GetBytes(coordinator)),
        };
        start.Environment["KOMBIFY_UPDATE_CLIENT"] = clientPath;
        start.Environment["KOMBIFY_UPDATE_LOG"] = logPath;
        start.Environment["KOMBIFY_UPDATE_PARENT_ID"] = parentId.ToString(System.Globalization.CultureInfo.InvariantCulture);
        start.Environment["KOMBIFY_UPDATE_COUNT"] = installers.Length.ToString(System.Globalization.CultureInfo.InvariantCulture);
        for (var index = 0; index < installers.Length; index++)
        {
            var prefix = $"KOMBIFY_UPDATE_{index}_";
            start.Environment[prefix + "PATH"] = installers[index].Path;
            start.Environment[prefix + "SHA256"] = installers[index].Sha256;
            start.Environment[prefix + "SIZE"] = installers[index].Size.ToString(System.Globalization.CultureInfo.InvariantCulture);
            start.Environment[prefix + "ARGUMENTS"] = installers[index].Arguments;
        }
        return start;
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
        if (!File.Exists(PendingPath())) return null;
        var pending = JsonSerializer.Deserialize<PendingUpdate>(File.ReadAllText(PendingPath()), JsonOptions);
        if (pending is null || pending.Phase is not (PhaseApplying or PhaseRollingBack) ||
            CompareVersions(pending.From, pending.From) is null || CompareVersions(pending.To, pending.To) is null)
            throw new InvalidDataException("The pending client recovery state is invalid.");
        return pending;
    }

    private static void WritePending(PendingUpdate pending)
    {
        Directory.CreateDirectory(UpdatesDirectory());
        var path = PendingPath();
        var temporary = path + $".{Guid.NewGuid():N}.tmp";
        try
        {
            using (var file = new FileStream(temporary, FileMode.CreateNew, FileAccess.Write, FileShare.None))
            {
                file.Write(JsonSerializer.SerializeToUtf8Bytes(pending, JsonOptions));
                file.Flush(flushToDisk: true);
            }
            File.Move(temporary, path, overwrite: true);
        }
        finally
        {
            File.Delete(temporary);
        }
    }

    internal static void CaptureUpdateSnapshot(ClientConfig config)
    {
        var source = DesktopEdition.IsLocal ? config.RuntimeDataDir : ClientConfig.StateDirectory();
        var destination = SnapshotDirectory();
        DeleteDirectory(destination);
        Directory.CreateDirectory(destination);
        if (Directory.Exists(source))
        {
            CopyDirectory(source, destination, DesktopEdition.IsLocal ? null : UpdatesDirectory());
        }
    }

    internal static void RestoreUpdateSnapshot(ClientConfig config) =>
        RestoreDirectory(SnapshotDirectory(), DesktopEdition.IsLocal ? config.RuntimeDataDir : ClientConfig.StateDirectory(),
            DesktopEdition.IsLocal ? null : UpdatesDirectory());

    private static void RestoreDirectory(string snapshot, string target, string? excludedDirectory = null)
    {
        Directory.CreateDirectory(target);
        foreach (var entry in Directory.EnumerateFileSystemEntries(target))
        {
            if (excludedDirectory is not null && Path.GetFullPath(entry).Equals(
                    Path.GetFullPath(excludedDirectory), StringComparison.OrdinalIgnoreCase)) continue;
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
    private static void CopyDirectory(string source, string destination, string? excludedDirectory = null)
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
            if (excludedDirectory is not null && Path.GetFullPath(directory).Equals(
                    Path.GetFullPath(excludedDirectory), StringComparison.OrdinalIgnoreCase)) continue;
            // Extracted PostgreSQL binaries are recreated from the installed
            // bundle at start; they are not data.
            var extractedBinaries = Path.GetFileName(directory).Equals("runtime", StringComparison.OrdinalIgnoreCase) &&
                Path.GetFileName(source).Equals("postgres", StringComparison.OrdinalIgnoreCase);
            if (!extractedBinaries && (File.GetAttributes(directory) & FileAttributes.ReparsePoint) == 0)
            {
                CopyDirectory(directory, Path.Combine(destination, Path.GetFileName(directory)), excludedDirectory);
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
        public string InstallerSha256 { get; init; } = "";
        public long InstallerSize { get; init; }
        public string RollbackSha256 { get; init; } = "";
        public long RollbackSize { get; init; }
        public string Phase { get; init; } = "";
        public DateTimeOffset StartedAt { get; init; }
    }

    private sealed record RecoveryInstaller(string Path, string Sha256, long Size);
    internal sealed record InstallerHandoff(string Path, string Sha256, long Size, string Arguments);

    internal sealed record VerifiedPackage
    {
        [JsonPropertyName("status")] public string Status { get; init; } = "";
        [JsonPropertyName("version")] public string Version { get; init; } = "";
        [JsonPropertyName("package_path")] public string PackagePath { get; init; } = "";
        [JsonPropertyName("package_sha256")] public string PackageSha256 { get; init; } = "";
        [JsonPropertyName("package_size")] public long PackageSize { get; init; }
    }
}
