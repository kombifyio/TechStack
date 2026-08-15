export const AUTH0_FORM_ERROR_PATTERN =
  /wrong email or password|invalid email or password|too many attempts|account.*blocked|user is blocked|verify your email|captcha|suspicious|rate limit|callback url mismatch|provided redirect_uri|allowed callback urls/i;

export function compactAuth0Error(value: string) {
  return value.replace(/\s+/g, " ").trim().slice(0, 240);
}
