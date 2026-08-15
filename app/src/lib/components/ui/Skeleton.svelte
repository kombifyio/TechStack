<script lang="ts">
  interface Props {
    class?: string;
    width?: string;
    height?: string;
    rounded?: "none" | "sm" | "md" | "lg" | "full";
  }

  let {
    class: className = "",
    width = "100%",
    height = "1rem",
    rounded = "md",
  }: Props = $props();

  const roundedClasses = {
    none: "",
    sm: "rounded-sm",
    md: "rounded-md",
    lg: "rounded-lg",
    full: "rounded-full",
  };
</script>

<div
  class="skeleton animate-pulse bg-gray-700/50 {roundedClasses[
    rounded
  ]} {className}"
  style="width: {width}; height: {height};"
  aria-hidden="true"
></div>

<style>
  .skeleton {
    background: linear-gradient(
      90deg,
      rgba(55, 65, 81, 0.5) 25%,
      rgba(75, 85, 99, 0.5) 50%,
      rgba(55, 65, 81, 0.5) 75%
    );
    background-size: 200% 100%;
    animation: shimmer 1.5s infinite;
  }

  @keyframes shimmer {
    0% {
      background-position: 200% 0;
    }
    100% {
      background-position: -200% 0;
    }
  }
</style>
