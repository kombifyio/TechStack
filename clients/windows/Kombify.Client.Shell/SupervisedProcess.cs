using System.Diagnostics;

namespace Kombify.Client.Shell;

/// <summary>Owns only the process started by this shell; a product supplies arguments, environment and health.</summary>
public sealed class SupervisedProcess : IDisposable
{
    private Process? _process;
    private bool _stopped;

    public bool HasExited => _stopped || _process?.HasExited == true;
    public int ExitCode => _process?.ExitCode ?? throw new InvalidOperationException("Process has not exited.");

    public void Start(ProcessStartInfo start, Action<string?> output)
    {
        if (_process is not null) throw new InvalidOperationException("The supervised process is already started.");
        start.UseShellExecute = false;
        start.RedirectStandardOutput = true;
        start.RedirectStandardError = true;
        var process = new Process { StartInfo = start, EnableRaisingEvents = true };
        process.OutputDataReceived += (_, eventArgs) => output(eventArgs.Data);
        process.ErrorDataReceived += (_, eventArgs) => output(eventArgs.Data);
        try
        {
            if (!process.Start()) throw new InvalidOperationException("The supervised process did not start.");
            _process = process;
            _stopped = false;
            process.BeginOutputReadLine();
            process.BeginErrorReadLine();
        }
        catch
        {
            process.Dispose();
            throw;
        }
    }

    public void Stop()
    {
        var process = _process;
        _process = null;
        _stopped = true;
        if (process is null) return;
        try
        {
            if (!process.HasExited)
            {
                process.Kill(entireProcessTree: true);
                process.WaitForExit(3000);
            }
        }
        catch (Exception error) when (error is InvalidOperationException or System.ComponentModel.Win32Exception) { }
        finally { process.Dispose(); }
    }

    public void Dispose() => Stop();
}
