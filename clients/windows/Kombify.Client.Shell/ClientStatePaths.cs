using System.Security.Cryptography;
using System.Text;

namespace Kombify.Client.Shell;

/// <summary>Product and edition scoped paths and credential targets for one Windows installation.</summary>
public sealed class ClientStatePaths(string product, string edition, string channel = "stable",
    string? existingDefaultDirectory = null, IReadOnlyList<string>? legacyOverridePrefixes = null)
{
    public string Product { get; } = ValidateSegment(product);
    public string Edition { get; } = ValidateSegment(edition);
    public string Channel { get; } = ValidateSegment(channel);

    public string DefaultDirectory => existingDefaultDirectory ?? Path.Combine(
        Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData),
        "kombify", Product, Edition, Channel);

    public string ResolveDirectory(string? overridePath)
    {
        if (string.IsNullOrWhiteSpace(overridePath)) return DefaultDirectory;
        if (!Path.IsPathRooted(overridePath)) return DefaultDirectory;

        var root = Path.GetFullPath(DefaultDirectory)
            .TrimEnd(Path.DirectorySeparatorChar, Path.AltDirectorySeparatorChar);
        var candidate = Path.GetFullPath(overridePath)
            .TrimEnd(Path.DirectorySeparatorChar, Path.AltDirectorySeparatorChar);
        var sameProductTestInstance = existingDefaultDirectory is not null
            && Path.GetDirectoryName(candidate)?.Equals(Path.GetDirectoryName(root), StringComparison.OrdinalIgnoreCase) == true
            && legacyOverridePrefixes?.Any(prefix => Path.GetFileName(candidate).StartsWith(prefix, StringComparison.OrdinalIgnoreCase)) == true;
        return candidate.Equals(root, StringComparison.OrdinalIgnoreCase)
            || candidate.StartsWith(root + Path.DirectorySeparatorChar, StringComparison.OrdinalIgnoreCase)
            || sameProductTestInstance
            ? candidate : DefaultDirectory;
    }

    public string CredentialTarget(string directory, string kind) =>
        CredentialTargetFor(directory, $"kombify/{Product}/{Edition}/{Channel}/{ValidateSegment(kind)}");

    public string CredentialTargetFor(string directory, string baseTarget)
    {
        var canonical = Path.GetFullPath(DefaultDirectory).TrimEnd(Path.DirectorySeparatorChar);
        var actual = Path.GetFullPath(directory).TrimEnd(Path.DirectorySeparatorChar);
        if (actual.Equals(canonical, StringComparison.OrdinalIgnoreCase)) return baseTarget;
        var digest = SHA256.HashData(Encoding.UTF8.GetBytes(actual.ToUpperInvariant()));
        return $"{baseTarget}/state-{Convert.ToHexString(digest).ToLowerInvariant()[..16]}";
    }

    private static string ValidateSegment(string value)
    {
        if (string.IsNullOrWhiteSpace(value) || value.Any(c => !char.IsAsciiLetterOrDigit(c) && c is not '-' and not '_'))
            throw new ArgumentException("A client state segment must use letters, digits, hyphens, or underscores.");
        return value.ToLowerInvariant();
    }
}
