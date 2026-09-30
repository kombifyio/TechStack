namespace Kombify.TechStack.Client;

// The edition is selected by the build; persisted configuration cannot turn a
// Local installation into a Cloud installation.
internal static class DesktopEdition
{
#if CLOUD_DESKTOP
    public const string Name = "cloud";
    public const string Channel = "beta";
    public static bool IsLocal => false;
#else
    public const string Name = "local";
    public const string Channel = "stable";
    public static bool IsLocal => true;
#endif
}
