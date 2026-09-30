using System.Security.Cryptography;

namespace Kombify.Client.Shell;

/// <summary>Copies a staged installer and rechecks the exact package digest before handoff.</summary>
public static class VerifiedUpdateHandoff
{
    public static void CopyVerified(string source, string destination, string expectedSha256, long expectedSize)
    {
        if (expectedSize <= 0 || expectedSha256.Length != 64 ||
            !expectedSha256.All(Uri.IsHexDigit))
            throw new ArgumentException("The update descriptor has an invalid digest or size.");

        using (var input = new FileStream(source, FileMode.Open, FileAccess.Read, FileShare.Read))
        using (var output = new FileStream(destination, FileMode.Create, FileAccess.Write, FileShare.None))
            input.CopyTo(output);

        using var copy = new FileStream(destination, FileMode.Open, FileAccess.Read, FileShare.Read);
        var digest = Convert.ToHexString(SHA256.HashData(copy));
        if (copy.Length == expectedSize && digest.Equals(expectedSha256, StringComparison.OrdinalIgnoreCase)) return;
        copy.Dispose();
        File.Delete(destination);
        throw new InvalidOperationException("The staged installer no longer matches its verified digest.");
    }
}
