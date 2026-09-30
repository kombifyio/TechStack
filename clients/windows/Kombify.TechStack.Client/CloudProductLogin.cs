#if CLOUD_DESKTOP
using System.Net;
using System.Net.Sockets;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using Microsoft.Web.WebView2.Core;

namespace Kombify.TechStack.Client;

internal sealed partial class ClientWindow
{
    private CancellationTokenSource? _cloudProductLoginCancellation;
    private readonly SemaphoreSlim _cloudProductLoginGate = new(1, 1);
    private readonly HttpClient _cloudProductHttpClient = new(new HttpClientHandler
        { AllowAutoRedirect = false, UseCookies = false }) { Timeout = TimeSpan.FromSeconds(15) };
    private static readonly Uri ProductOrigin = new(ClientAuthority.CloudOrigin);
    private const string SessionCookie = "techstack_session";
    private const string OriginCookie = "techstack_session_origin";

    private async Task BeginCloudProductLoginAsync()
    {
        _cloudProductLoginCancellation?.Cancel();
        var attempt = new CancellationTokenSource(TimeSpan.FromMinutes(5));
        _cloudProductLoginCancellation = attempt;
        await _cloudProductLoginGate.WaitAsync();
        try
        {
            if (!ReferenceEquals(_cloudProductLoginCancellation, attempt)) return;
            await RunCloudProductLoginAsync(attempt);
        }
        finally
        {
            _cloudProductLoginGate.Release();
            if (ReferenceEquals(_cloudProductLoginCancellation, attempt))
                _cloudProductLoginCancellation = null;
            attempt.Dispose();
        }
    }

    private async Task RunCloudProductLoginAsync(CancellationTokenSource attempt)
    {
        var token = attempt.Token;
        TcpListener? listener = null;
        var accountMismatch = false;
        try
        {
            for (var port = 63690; port <= 63694; port++)
            {
                try
                {
                    listener = new TcpListener(IPAddress.Loopback, port);
                    listener.Start(1);
                    break;
                }
                catch (SocketException)
                {
                    listener?.Stop();
                    listener = null;
                }
            }
            if (listener is null)
                throw new InvalidOperationException("No approved loopback login port is available.");

            var verifier = Base64Url(RandomNumberGenerator.GetBytes(32));
            var challenge = Base64Url(SHA256.HashData(Encoding.ASCII.GetBytes(verifier)));
            using var startContent = JsonContent(new
            {
                verifier_challenge = challenge,
                callback_port = ((IPEndPoint)listener.LocalEndpoint).Port,
            });
            using var start = await _cloudProductHttpClient.PostAsync(new Uri(ProductOrigin, "/api/v2/auth/native/start"), startContent, token);
            if (!start.IsSuccessStatusCode)
                throw new InvalidOperationException("Techstack could not start this browser login.");
            using var startJSON = JsonDocument.Parse(await start.Content.ReadAsStringAsync(token));
            var state = startJSON.RootElement.GetProperty("state").GetString() ?? "";
            var loginURL = startJSON.RootElement.GetProperty("login_url").GetString() ?? "";
            if (state.Length != 43 || !Uri.TryCreate(loginURL, UriKind.Absolute, out var loginUri)
                || !ClientAuthority.SameOrigin(loginUri, ProductOrigin)
                || loginUri.AbsolutePath != "/api/v2/auth/login")
                throw new InvalidOperationException("Techstack returned an invalid browser login URL.");

            NavigateHtml(RenderFallback(CloudLoginText.Get("WaitingTitle"),
                CloudLoginText.Get("WaitingDetail")));
            OpenInBrowser(loginURL);
            var ticket = await AwaitProductCallbackAsync(listener, state, token);
            using var redeemContent = JsonContent(new { ticket, verifier, state });
            using var redeem = await _cloudProductHttpClient.PostAsync(new Uri(ProductOrigin, "/api/v2/auth/native/redeem"), redeemContent, token);
            if (!redeem.IsSuccessStatusCode)
                throw new InvalidOperationException("The browser login could not be transferred to this client.");
            using var identityJSON = JsonDocument.Parse(await redeem.Content.ReadAsStringAsync(token));
            var subject = identityJSON.RootElement.GetProperty("subject").GetString() ?? "";
            var tenant = identityJSON.RootElement.GetProperty("tenantId").GetString() ?? "";
            if (subject.Length == 0 || tenant.Length == 0)
                throw new InvalidOperationException("The transferred login has no product identity.");

            var identityHash = Convert.ToHexString(SHA256.HashData(Encoding.UTF8.GetBytes(subject + "\n" + tenant)));
            var markerPath = Path.Combine(ClientConfig.StateDirectory(), "webview2-cloud-product-v1", "identity");
            if (File.Exists(markerPath) && File.ReadAllText(markerPath).Trim() != identityHash)
            {
                accountMismatch = true;
                throw new InvalidOperationException("Cloud product identity mismatch");
            }
            if (!File.Exists(markerPath))
            {
                Directory.CreateDirectory(Path.GetDirectoryName(markerPath)!);
                using var marker = new FileStream(markerPath, FileMode.CreateNew, FileAccess.Write, FileShare.None);
                marker.Write(Encoding.ASCII.GetBytes(identityHash + "\n"));
                marker.Flush(flushToDisk: true);
            }

            var cookies = ParseProductCookies(redeem);
            token.ThrowIfCancellationRequested();
            await ClearProductCookiesAsync();
            token.ThrowIfCancellationRequested();
            ImportProductCookies(cookies);
            await VerifyProductCookiesAsync(cookies);
            token.ThrowIfCancellationRequested();
            await VerifyProductIdentityAsync(cookies, subject, tenant, token);
            token.ThrowIfCancellationRequested();
            _webView.CoreWebView2.Navigate(new Uri(ProductOrigin, "/dashboard").ToString());
        }
        catch (OperationCanceledException)
        {
            // The gate keeps a replacement attempt from importing until cleanup ends.
            if (!IsDisposed) await ClearProductCookiesAsync();
            if (!IsDisposed && ReferenceEquals(_cloudProductLoginCancellation, attempt))
                NavigateHtml(RenderFallback(CloudLoginText.Get("FailureTitle"),
                    CloudLoginText.Get("FailureDetail")));
        }
        catch (Exception)
        {
            if (!IsDisposed) await ClearProductCookiesAsync();
            if (!IsDisposed && ReferenceEquals(_cloudProductLoginCancellation, attempt))
                NavigateHtml(RenderFallback(CloudLoginText.Get("FailureTitle"),
                    CloudLoginText.Get(accountMismatch ? "AccountMismatch" : "FailureDetail")));
        }
        finally
        {
            listener?.Stop();
        }
    }

    private static string Base64Url(byte[] bytes) => Convert.ToBase64String(bytes)
        .TrimEnd('=').Replace('+', '-').Replace('/', '_');

    private static async Task<string> AwaitProductCallbackAsync(TcpListener listener, string expectedState,
        CancellationToken token)
    {
        while (true)
        {
            using var client = await listener.AcceptTcpClientAsync(token);
            using var stream = client.GetStream();
            using var reader = new StreamReader(stream, Encoding.ASCII, leaveOpen: true);
            var requestLine = await reader.ReadLineAsync(token) ?? "";
            var host = "";
            for (var i = 0; i < 32; i++)
            {
                var line = await reader.ReadLineAsync(token);
                if (line is null || line.Length == 0) break;
                if (line.StartsWith("Host:", StringComparison.OrdinalIgnoreCase)) host = line[5..].Trim();
            }
            var expectedHost = "127.0.0.1:" + ((IPEndPoint)listener.LocalEndpoint).Port;
            var parts = requestLine.Split(' ', 3);
            string? ticket = null;
            if (parts.Length == 3 && parts[0] == "GET" && host == expectedHost
                && parts[1].Length < 2048
                && Uri.TryCreate("http://" + expectedHost + parts[1], UriKind.Absolute, out var callback)
                && callback.AbsolutePath == "/oauth/callback")
            {
                var query = System.Web.HttpUtility.ParseQueryString(callback.Query);
                if (query.Get("state") == expectedState)
                    ticket = query.Get("ticket");
            }
            var success = ticket is { Length: 43 };
            var body = CloudLoginText.Get(success ? "CallbackSuccess" : "CallbackFailure");
            var status = success ? "200 OK" : "400 Bad Request";
            var bytes = Encoding.UTF8.GetBytes($"HTTP/1.1 {status}\r\nContent-Type: text/plain; charset=utf-8\r\nCache-Control: no-store\r\nContent-Length: {Encoding.UTF8.GetByteCount(body)}\r\nConnection: close\r\n\r\n{body}");
            await stream.WriteAsync(bytes, token);
            if (success) return ticket!;
        }
    }

    private sealed record ProductCookie(string Name, string Value, int MaxAge);

    private static IReadOnlyDictionary<string, ProductCookie> ParseProductCookies(HttpResponseMessage response)
    {
        if (!response.Headers.TryGetValues("Set-Cookie", out var values))
            throw new InvalidOperationException("The product session cookies are missing.");
        var cookies = new Dictionary<string, ProductCookie>(StringComparer.Ordinal);
        var headerCount = 0;
        foreach (var line in values)
        {
            if (++headerCount > 16 || line.Length > 8192)
                throw new InvalidOperationException("The product session cookie headers are invalid.");
            var parts = line.Split(';', StringSplitOptions.TrimEntries | StringSplitOptions.RemoveEmptyEntries);
            if (parts.Length == 0) continue;
            var first = parts[0].Split('=', 2);
            if (first.Length != 2 || first[0] is not (SessionCookie or OriginCookie))
                continue;
            if (cookies.ContainsKey(first[0]) || first[1].Length == 0
                || !parts.Any(p => p.Equals("Secure", StringComparison.OrdinalIgnoreCase))
                || !parts.Any(p => p.Equals("HttpOnly", StringComparison.OrdinalIgnoreCase))
                || !parts.Any(p => p.Equals("Path=/", StringComparison.OrdinalIgnoreCase))
                || !parts.Any(p => p.Equals("SameSite=Lax", StringComparison.OrdinalIgnoreCase))
                || parts.Any(p => p.StartsWith("Domain=", StringComparison.OrdinalIgnoreCase)))
                throw new InvalidOperationException("The product session cookie attributes are invalid.");
            var agePart = parts.FirstOrDefault(p => p.StartsWith("Max-Age=", StringComparison.OrdinalIgnoreCase));
            if (agePart is null || !int.TryParse(agePart[8..], out var maxAge) || maxAge <= 0)
                throw new InvalidOperationException("The product session cookie has no valid expiry.");
            cookies.Add(first[0], new ProductCookie(first[0], first[1], maxAge));
        }
        if (cookies.Count != 2) throw new InvalidOperationException("The product session cookies are incomplete.");
        return cookies;
    }

    private async Task ClearProductCookiesAsync()
    {
        if (_webView.CoreWebView2 is null) return;
        foreach (var cookie in await _webView.CoreWebView2.CookieManager.GetCookiesAsync(ProductOrigin.ToString()))
            if (cookie.Name is SessionCookie or OriginCookie)
                _webView.CoreWebView2.CookieManager.DeleteCookie(cookie);
    }

    private void ImportProductCookies(IReadOnlyDictionary<string, ProductCookie> cookies)
    {
        foreach (var entry in cookies.Values)
        {
            var cookie = _webView.CoreWebView2.CookieManager.CreateCookie(entry.Name, entry.Value,
                ProductOrigin.Host, "/");
            cookie.IsSecure = true;
            cookie.IsHttpOnly = true;
            cookie.SameSite = CoreWebView2CookieSameSiteKind.Lax;
            cookie.Expires = DateTime.Now.AddSeconds(entry.MaxAge);
            _webView.CoreWebView2.CookieManager.AddOrUpdateCookie(cookie);
        }
    }

    private async Task VerifyProductCookiesAsync(IReadOnlyDictionary<string, ProductCookie> expected)
    {
        var actual = await _webView.CoreWebView2.CookieManager.GetCookiesAsync(ProductOrigin.ToString());
        foreach (var entry in expected.Values)
            if (!actual.Any(c => c.Name == entry.Name && c.Value == entry.Value))
                throw new InvalidOperationException("The WebView rejected the product session.");
    }

    private async Task VerifyProductIdentityAsync(IReadOnlyDictionary<string, ProductCookie> cookies,
        string subject, string tenant, CancellationToken token)
    {
        using var request = new HttpRequestMessage(HttpMethod.Get, new Uri(ProductOrigin, "/api/v2/whoami"));
        request.Headers.TryAddWithoutValidation("Cookie",
            string.Join("; ", cookies.Values.Select(c => c.Name + "=" + c.Value)));
        using var response = await _cloudProductHttpClient.SendAsync(request, token);
        if (!response.IsSuccessStatusCode)
            throw new InvalidOperationException("Techstack did not accept the transferred session.");
        using var whoami = JsonDocument.Parse(await response.Content.ReadAsStringAsync(token));
        if (whoami.RootElement.GetProperty("subject").GetString() != subject
            || whoami.RootElement.GetProperty("tenantId").GetString() != tenant)
            throw new InvalidOperationException("The transferred session belongs to a different product identity.");
    }

    private async Task<ConnectProductIdentity> CurrentConnectIdentityAsync(CancellationToken token)
    {
        if (IsDisposed || _webView.CoreWebView2 is null)
            throw new ConnectAccountException();
        var actual = await _webView.CoreWebView2.CookieManager.GetCookiesAsync(ProductOrigin.ToString());
        var cookies = new Dictionary<string, string>(StringComparer.Ordinal);
        foreach (var cookie in actual.Where(c => c.Name is SessionCookie or OriginCookie))
        {
            if (cookie.Domain != ProductOrigin.Host || cookie.Path != "/" || !cookie.IsSecure
                || !cookie.IsHttpOnly || cookie.SameSite != CoreWebView2CookieSameSiteKind.Lax
                || cookie.Expires <= DateTime.Now
                || !cookies.TryAdd(cookie.Name, cookie.Value))
                throw new ConnectAccountException();
        }
        if (cookies.Count != 2 || cookies.Values.Any(string.IsNullOrEmpty))
            throw new ConnectAccountException();

        using var request = new HttpRequestMessage(HttpMethod.Get, new Uri(ProductOrigin, "/api/v2/whoami"));
        request.Headers.TryAddWithoutValidation("Cookie", string.Join("; ",
            cookies.Select(c => c.Key + "=" + c.Value)));
        using var response = await _cloudProductHttpClient.SendAsync(request, token);
        if (!response.IsSuccessStatusCode) throw new ConnectAccountException();
        using var document = JsonDocument.Parse(await response.Content.ReadAsStringAsync(token));
        var subject = document.RootElement.GetProperty("subject").GetString();
        var tenant = document.RootElement.GetProperty("tenantId").GetString();
        if (string.IsNullOrWhiteSpace(subject) || string.IsNullOrWhiteSpace(tenant))
            throw new ConnectAccountException();
        var expectedMarker = Convert.ToHexString(SHA256.HashData(
            Encoding.UTF8.GetBytes(subject + "\n" + tenant)));
        var markerPath = Path.Combine(ClientConfig.StateDirectory(), "webview2-cloud-product-v1", "identity");
        if (!File.Exists(markerPath) || File.ReadAllText(markerPath).Trim() != expectedMarker)
            throw new ConnectAccountException();
        var latest = await _webView.CoreWebView2.CookieManager.GetCookiesAsync(ProductOrigin.ToString());
        var latestProduct = latest.Where(c => c.Name is SessionCookie or OriginCookie).ToArray();
        if (latestProduct.Length != 2 || latestProduct.Any(c => c.Domain != ProductOrigin.Host
            || c.Path != "/" || !c.IsSecure || !c.IsHttpOnly
            || c.SameSite != CoreWebView2CookieSameSiteKind.Lax || c.Expires <= DateTime.Now
            || !cookies.TryGetValue(c.Name, out var value) || c.Value != value))
            throw new ConnectAccountException();
        return new ConnectProductIdentity(subject, tenant);
    }
}
#endif
