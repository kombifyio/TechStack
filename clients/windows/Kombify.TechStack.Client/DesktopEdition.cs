namespace Kombify.TechStack.Client;

// The edition is selected by the build; persisted configuration cannot turn a
// Local installation into a Cloud installation.
internal static class DesktopEdition
{
#if CLOUD_DESKTOP
    public const string Name = "cloud";
    public const string Channel = "beta";
    public const string UpdateManifestUrl = "https://github.com/kombifyio/TechStack/releases/download/windows-cloud-beta/kombify-techstack-windows-cloud-beta.json";
    public const string InstallerFileName = "kombify-Techstack-Cloud-Setup.exe";
    public const string VerifierFileName = "kombify-techstack-cloud-updater.exe";
    public const string InstallDirectoryName = "techstack-cloud";
    public static bool IsLocal => false;
#else
    public const string Name = "local";
    public const string Channel = "stable";
    public const string UpdateManifestUrl = "https://github.com/kombifyio/TechStack/releases/latest/download/kombify-techstack-windows-update.json";
    public const string InstallerFileName = "kombify-Techstack-Setup.exe";
    public const string VerifierFileName = "techstack.exe";
    public const string InstallDirectoryName = "techstack";
    public static bool IsLocal => true;
#endif
}
