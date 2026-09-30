namespace Kombify.Client.Shell;

/// <summary>Bounded local log read for a product's own redacted diagnostics surface.</summary>
public static class ClientDiagnostics
{
    public static string ReadTail(string path, int maxChars = 4000)
    {
        if (maxChars is < 1 or > 65536) throw new ArgumentOutOfRangeException(nameof(maxChars));
        try
        {
            if (!File.Exists(path)) return "";
            using var reader = new StreamReader(new FileStream(path, FileMode.Open, FileAccess.Read, FileShare.ReadWrite));
            var buffer = new char[maxChars];
            var count = 0;
            while (!reader.EndOfStream)
            {
                var next = reader.Read();
                if (next < 0) break;
                buffer[count % maxChars] = (char)next;
                count++;
            }
            var kept = Math.Min(count, maxChars);
            if (count <= maxChars) return new string(buffer, 0, kept);
            var start = count % maxChars;
            return new string(buffer, start, maxChars - start) + new string(buffer, 0, start);
        }
        catch (IOException) { return ""; }
        catch (UnauthorizedAccessException) { return ""; }
    }
}
