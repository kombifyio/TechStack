/** Keep newly revealed wizard content in view without moving keyboard focus. */
export function revealWizardDetails(anchor: HTMLElement | null | undefined) {
  if (!anchor?.isConnected) return;
  const { top } = anchor.getBoundingClientRect();
  if (top >= window.innerHeight * 0.25 && top <= window.innerHeight * 0.55)
    return;
  anchor.scrollIntoView({
    block: "center",
    inline: "nearest",
    behavior: window.matchMedia("(prefers-reduced-motion: reduce)").matches
      ? "instant"
      : "smooth",
  });
}
