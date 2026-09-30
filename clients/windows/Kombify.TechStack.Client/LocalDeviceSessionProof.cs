using System.Net;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;

namespace Kombify.TechStack.Client;

// The credential never crosses HTTP. Both replies are authenticated before a
// session cookie can enter WebView2, even if another listener takes the port.
internal static class LocalDeviceSessionProof
{
    internal const string ChallengePath = "/api/v1/auth/device-session-proof/challenge";
    internal const string SessionPath = "/api/v1/auth/device-session-proof";
    private const string ProofHeader = "X-TechStack-Device-Proof";

    internal static async Task<string[]> AuthenticateAsync(HttpClient http, Uri authority, string secret,
        CancellationToken cancellationToken = default)
    {
        using var timeout = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken);
        timeout.CancelAfter(TimeSpan.FromSeconds(15));
        var proofCancellation = timeout.Token;
        if (authority.Scheme != Uri.UriSchemeHttp || !authority.IsLoopback
            || authority.AbsolutePath != "/" || authority.Query.Length != 0 || authority.Fragment.Length != 0
            || authority.UserInfo.Length != 0 || secret.Length < 64)
            throw new InvalidOperationException("Invalid local device authority.");

        var origin = authority.GetLeftPart(UriPartial.Authority);
        var clientNonce = Convert.ToHexString(RandomNumberGenerator.GetBytes(16)).ToLowerInvariant();
        using var challengeRequest = JsonRequest(new Uri(authority, ChallengePath),
            new { origin, client_nonce = clientNonce });
        using var challengeResponse = await http.SendAsync(challengeRequest,
            HttpCompletionOption.ResponseHeadersRead, proofCancellation);
        if (challengeResponse.StatusCode != HttpStatusCode.OK)
            throw new InvalidOperationException("Local runtime identity proof was unavailable.");
        if (challengeResponse.Content.Headers.ContentLength is > 2048)
            throw new InvalidOperationException("Local runtime identity proof is too large.");
        using var challengeStream = await challengeResponse.Content.ReadAsStreamAsync(proofCancellation);
        var bounded = new byte[2049];
        var length = 0;
        while (length < bounded.Length)
        {
            var read = await challengeStream.ReadAsync(bounded.AsMemory(length), proofCancellation);
            if (read == 0) break;
            length += read;
        }
        if (length > 2048)
            throw new InvalidOperationException("Local runtime identity proof is too large.");
        using var challengeBody = JsonDocument.Parse(bounded.AsMemory(0, length));
        var data = challengeBody.RootElement.GetProperty("data");
        var serverNonce = data.GetProperty("server_nonce").GetString() ?? "";
        var serverProof = data.GetProperty("proof").GetString() ?? "";
        if (!ValidNonce(serverNonce) || !Matches(secret, Message("server", origin, clientNonce, serverNonce), serverProof))
            throw new InvalidOperationException("Local runtime identity proof did not verify.");

        using var sessionRequest = JsonRequest(new Uri(authority, SessionPath),
            new { origin, client_nonce = clientNonce, server_nonce = serverNonce });
        sessionRequest.Headers.TryAddWithoutValidation(ProofHeader,
            Mac(secret, Message("client", origin, clientNonce, serverNonce, "POST", SessionPath)));
        using var sessionResponse = await http.SendAsync(sessionRequest,
            HttpCompletionOption.ResponseHeadersRead, proofCancellation);
        var cookieHeaders = sessionResponse.Headers.TryGetValues("Set-Cookie", out var cookies)
            ? cookies.ToArray() : [];
        Array.Sort(cookieHeaders, StringComparer.Ordinal);
        if (sessionResponse.StatusCode != HttpStatusCode.OK
            || cookieHeaders.Length != 2
            || !cookieHeaders.Any(cookie => cookie.StartsWith("techstack_session=", StringComparison.Ordinal))
            || !cookieHeaders.Any(cookie => cookie.StartsWith("techstack_session_origin=", StringComparison.Ordinal))
            || cookieHeaders.Any(cookie => !cookie.Contains("HttpOnly", StringComparison.OrdinalIgnoreCase))
            || !sessionResponse.Headers.TryGetValues(ProofHeader, out var proofs)
            || proofs.SingleOrDefault() is not { } responseProof
            || !Matches(secret, Message("response", origin, clientNonce, serverNonce, "200",
                string.Join("\n", cookieHeaders)), responseProof))
            throw new InvalidOperationException("Local device session did not verify.");

        return cookieHeaders;
    }

    private static HttpRequestMessage JsonRequest(Uri endpoint, object payload) => new(HttpMethod.Post, endpoint)
    {
        Content = new StringContent(JsonSerializer.Serialize(payload), Encoding.UTF8, "application/json")
    };

    private static bool ValidNonce(string nonce) => nonce.Length == 32
        && nonce.All(c => c is >= '0' and <= '9' or >= 'a' and <= 'f');

    private static string Message(params string[] parts) =>
        "techstack-local-device/v1\n" + string.Join('\n', parts);

    private static string Mac(string secret, string message) => Convert.ToHexString(
        HMACSHA256.HashData(Encoding.UTF8.GetBytes(secret), Encoding.UTF8.GetBytes(message))).ToLowerInvariant();

    private static bool Matches(string secret, string message, string provided)
    {
        if (provided.Length != 64 || !provided.All(c => c is >= '0' and <= '9' or >= 'a' and <= 'f'))
            return false;
        return CryptographicOperations.FixedTimeEquals(
            Convert.FromHexString(Mac(secret, message)), Convert.FromHexString(provided));
    }
}
