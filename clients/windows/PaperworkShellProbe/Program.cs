using Kombify.Client.Shell;

// A runnable package-consumer probe. Paperwork's eventual product shell owns
// its UI, runtime arguments, identity and mail/document stores.
var paths = new ClientStatePaths("paperwork", "cloud", "beta");
var state = paths.ResolveDirectory(null);
if (!state.EndsWith(Path.Combine("kombify", "paperwork", "cloud", "beta"), StringComparison.OrdinalIgnoreCase))
    throw new InvalidOperationException("Paperwork Cloud state escaped its edition namespace.");
var target = paths.CredentialTarget(state, "session");
if (target != "kombify/paperwork/cloud/beta/session")
    throw new InvalidOperationException("Paperwork session custody has the wrong product or edition.");

// Cloud-only consumer: exercise the packaged Windows custody implementation
// with a disposable key, without an account, token or remote enrollment.
var keyName = "kombify/paperwork/cloud/beta/package-probe-" + Guid.NewGuid();
try
{
    using var key = ConnectDeviceKey.OpenOrCreate(keyName);
    var proof = key.CreateProof("POST", new Uri("https://connect.kombify.io/v1/installations"));
    if (!key.VerifyProof(proof)) throw new InvalidOperationException("Packaged custody failed to sign a valid proof.");
}
finally { ConnectDeviceKey.Delete(keyName); }

Console.WriteLine("Paperwork Cloud package consumer: state isolation and Windows DPoP custody PASS");
