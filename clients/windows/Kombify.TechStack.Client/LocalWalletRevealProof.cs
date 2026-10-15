using System.Net;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;

namespace Kombify.TechStack.Client;

internal sealed record LocalWalletSession(string Session, string Origin, string Csrf)
{
    internal string Digest => LocalWalletRevealProof.Hash("techstack_session=" + Session + "\ntechstack_session_origin=" + Origin);
    internal string CookieHeader(string csrf) => "techstack_session=" + Session + "; techstack_session_origin=" + Origin + "; csrf_token=" + csrf;
}

internal sealed record LocalWalletChallenge(string Origin, string OwnerId, string TenantId, string Provider,
    string ItemId, string Snapshot, string ReasonDigest, string SessionDigest, string DisplayName,
    string ClientNonce, string ServerNonce, string ExpiresAt, string Proof);

internal sealed record LocalWalletResult(string Id, string? Secret, string? Totp, string RevealedAt);

// Only this operation can use the device credential. Intermediate proofs,
// cookies and the device token never enter the WebView document.
internal static class LocalWalletRevealProof
{
    private static readonly JsonSerializerOptions Json = new() { PropertyNamingPolicy = JsonNamingPolicy.SnakeCaseLower };

    internal static async Task<LocalWalletResult?> RevealAsync(HttpClient http, Uri authority, string secret,
        LocalWalletSession session, string itemId, string reason,
        Func<LocalWalletChallenge, CancellationToken, Task<bool>> confirm,
        Func<CancellationToken, Task<LocalWalletSession?>> currentSession,
        CancellationToken cancellationToken = default)
    {
        if (authority.Scheme != "http" || !authority.IsLoopback || authority.AbsolutePath != "/"
            || authority.UserInfo.Length != 0 || authority.Query.Length != 0 || authority.Fragment.Length != 0
            || secret.Length < 64 || !ValidItemId(itemId) || reason.Length > 1024
            || string.IsNullOrEmpty(session.Session) || string.IsNullOrEmpty(session.Origin))
            throw new InvalidOperationException(LocalRuntimeMessages.WalletRevealFailed);
        using var timeout = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken);
        timeout.CancelAfter(TimeSpan.FromSeconds(150));
        var ct = timeout.Token;
        var csrf = session.Csrf;
        if (string.IsNullOrEmpty(csrf))
        {
            using var csrfRequest = new HttpRequestMessage(HttpMethod.Get, new Uri(authority, "/api/v1/csrf"));
            csrfRequest.Headers.TryAddWithoutValidation("Cookie", session.CookieHeader(""));
            using var csrfResponse = await http.SendAsync(csrfRequest, HttpCompletionOption.ResponseHeadersRead, ct);
            if (csrfResponse.StatusCode != HttpStatusCode.OK) throw new InvalidOperationException(LocalRuntimeMessages.WalletRevealFailed);
            var csrfCookies = csrfResponse.Headers.TryGetValues("Set-Cookie", out var cookies)
                ? cookies.Select(value => value.Split(';')[0]).Where(value => value.StartsWith("csrf_token=", StringComparison.Ordinal)).ToArray() : [];
            var csrfHeaders = csrfResponse.Headers.TryGetValues("X-CSRF-Token", out var tokens) ? tokens.ToArray() : [];
            // CSRF middleware and the endpoint can both issue the cookie; use
            // the endpoint's final value and require the matching cookie.
            if (csrfHeaders.Length != 1 || csrfHeaders[0].Length != 64
                || !csrfHeaders[0].All(c => c is >= '0' and <= '9' or >= 'a' and <= 'f')
                || csrfCookies.LastOrDefault() != "csrf_token=" + csrfHeaders[0])
                throw new InvalidOperationException(LocalRuntimeMessages.WalletRevealFailed);
            csrf = csrfHeaders[0];
        }
        var nonce = Convert.ToHexString(RandomNumberGenerator.GetBytes(16)).ToLowerInvariant();
        var path = "/api/v1/wallet/" + Uri.EscapeDataString(itemId);
        using var challengeRequest = Request(new Uri(authority, path + "/device-reauth/challenge"), session, csrf,
            new { reason, client_nonce = nonce });
        using var challengeResponse = await http.SendAsync(challengeRequest, HttpCompletionOption.ResponseHeadersRead, ct);
        using var challengeBody = await ReadResponse(challengeResponse, ct);
        var challenge = RequiredData(challengeBody).Deserialize<LocalWalletChallenge>(Json)
            ?? throw new InvalidOperationException(LocalRuntimeMessages.WalletRevealFailed);
        if (new[] { challenge.Origin, challenge.OwnerId, challenge.TenantId, challenge.Provider, challenge.ItemId,
            challenge.Snapshot, challenge.ReasonDigest, challenge.SessionDigest, challenge.DisplayName,
            challenge.ClientNonce, challenge.ServerNonce, challenge.ExpiresAt, challenge.Proof }.Any(string.IsNullOrEmpty))
            throw new InvalidOperationException(LocalRuntimeMessages.WalletRevealFailed);
        if (challenge.Origin != authority.GetLeftPart(UriPartial.Authority) || challenge.ItemId != itemId
            || challenge.ClientNonce != nonce || !ValidNonce(challenge.ServerNonce)
            || challenge.SessionDigest != session.Digest || challenge.ReasonDigest != Hash(reason)
            || string.IsNullOrEmpty(challenge.OwnerId) || string.IsNullOrEmpty(challenge.TenantId)
            || !DateTimeOffset.TryParse(challenge.ExpiresAt, out var expiry) || expiry <= DateTimeOffset.UtcNow
            || expiry > DateTimeOffset.UtcNow.AddMinutes(3)
            || !Matches(secret, Transcript(challenge, "challenge"), challenge.Proof))
            throw new InvalidOperationException(LocalRuntimeMessages.WalletRevealFailed);
        if (!await confirm(challenge, ct)) return null;
        ct.ThrowIfCancellationRequested();
        if ((await currentSession(ct))?.Digest != session.Digest)
            throw new InvalidOperationException(LocalRuntimeMessages.WalletRevealFailed);
        using var revealRequest = Request(new Uri(authority, path + "/reveal"), session, csrf, new
        {
            reason,
            device_proof = new { client_nonce = nonce, server_nonce = challenge.ServerNonce,
                proof = Mac(secret, Transcript(challenge, "reveal", "POST", "/api/v1/wallet/" + itemId + "/reveal")) }
        });
        using var response = await http.SendAsync(revealRequest, HttpCompletionOption.ResponseHeadersRead, ct);
        using var responseBody = await ReadResponse(response, ct);
        var data = RequiredData(responseBody);
        var payload = RequiredString(data, "payload");
        var proof = RequiredString(data, "proof");
        if (!Matches(secret, Transcript(challenge, "result", "200", payload), proof))
            throw new InvalidOperationException(LocalRuntimeMessages.WalletRevealFailed);
        var bytes = Convert.FromBase64String(payload.Replace('-', '+').Replace('_', '/').PadRight(payload.Length + (4 - payload.Length % 4) % 4, '='));
        var result = JsonSerializer.Deserialize<LocalWalletResult>(bytes, Json);
        if (result is null || result.Id != itemId || string.IsNullOrEmpty(result.RevealedAt) || (await currentSession(ct))?.Digest != session.Digest)
            throw new InvalidOperationException(LocalRuntimeMessages.WalletRevealFailed);
        return result;
    }

    private static JsonElement RequiredData(JsonDocument document)
    {
        if (document.RootElement.ValueKind != JsonValueKind.Object || !document.RootElement.TryGetProperty("data", out var data)
            || data.ValueKind != JsonValueKind.Object) throw new InvalidOperationException(LocalRuntimeMessages.WalletRevealFailed);
        return data;
    }

    private static string RequiredString(JsonElement data, string name)
    {
        if (!data.TryGetProperty(name, out var value) || value.ValueKind != JsonValueKind.String
            || string.IsNullOrEmpty(value.GetString())) throw new InvalidOperationException(LocalRuntimeMessages.WalletRevealFailed);
        return value.GetString()!;
    }

    private static HttpRequestMessage Request(Uri uri, LocalWalletSession session, string csrf, object body)
    {
        var request = new HttpRequestMessage(HttpMethod.Post, uri)
        { Content = new StringContent(JsonSerializer.Serialize(body), Encoding.UTF8, "application/json") };
        request.Headers.TryAddWithoutValidation("Cookie", session.CookieHeader(csrf));
        request.Headers.TryAddWithoutValidation("X-CSRF-Token", csrf);
        return request;
    }

    private static async Task<JsonDocument> ReadResponse(HttpResponseMessage response, CancellationToken ct)
    {
        if (response.StatusCode != HttpStatusCode.OK || response.Content.Headers.ContentLength is > 524288)
            throw new InvalidOperationException(LocalRuntimeMessages.WalletRevealFailed);
        using var stream = await response.Content.ReadAsStreamAsync(ct);
        using var bytes = new MemoryStream();
        var buffer = new byte[8192];
        while (true)
        {
            var read = await stream.ReadAsync(buffer, ct);
            if (read == 0) break;
            if (bytes.Length + read > 524288) throw new InvalidOperationException(LocalRuntimeMessages.WalletRevealFailed);
            bytes.Write(buffer, 0, read);
        }
        return JsonDocument.Parse(bytes.ToArray());
    }

    internal static string Hash(string value) => Convert.ToHexString(SHA256.HashData(Encoding.UTF8.GetBytes(value))).ToLowerInvariant();
    internal static bool ValidItemId(string? itemId) => !string.IsNullOrWhiteSpace(itemId) && itemId.Length <= 512
        && itemId == itemId.Trim() && itemId is not "." and not ".." && !itemId.Any(char.IsControl)
        && itemId.IndexOfAny(['/', '\\']) < 0;
    private static bool ValidNonce(string value) => value.Length == 32 && value.All(c => c is >= '0' and <= '9' or >= 'a' and <= 'f');
    internal static string Transcript(LocalWalletChallenge c, string action, params string[] extra)
    {
        string[] fields = [action, c.Origin, c.OwnerId, c.TenantId, c.Provider, c.ItemId, c.Snapshot,
            c.ReasonDigest, c.SessionDigest, c.DisplayName, c.ClientNonce, c.ServerNonce, c.ExpiresAt, .. extra];
        return "techstack-wallet-device/v1" + string.Concat(fields.Select(value => Encoding.UTF8.GetByteCount(value) + ":" + value));
    }
    internal static string Mac(string secret, string message) => Convert.ToHexString(HMACSHA256.HashData(
        Encoding.UTF8.GetBytes(secret), Encoding.UTF8.GetBytes(message))).ToLowerInvariant();
    private static bool Matches(string secret, string message, string proof) => proof.Length == 64
        && proof.All(c => c is >= '0' and <= '9' or >= 'a' and <= 'f')
        && CryptographicOperations.FixedTimeEquals(Convert.FromHexString(Mac(secret, message)), Convert.FromHexString(proof));
}
