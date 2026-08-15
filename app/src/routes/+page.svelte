<script lang="ts">
  // Auth redirect is now handled by +page.ts load function
  // This page shows a loading state while the redirect happens
</script>

<div class="min-h-full flex items-center justify-center bg-[var(--color-void)]">
  <div class="text-center">
    <div class="text-6xl mb-4 text-primary">
      <svg
        class="w-16 h-16 mx-auto animate-pulse"
        fill="none"
        stroke="currentColor"
        viewBox="0 0 24 24"
      >
        <path
          stroke-linecap="round"
          stroke-linejoin="round"
          stroke-width="1.5"
          d="M13 10V3L4 14h7v7l9-11h-7z"
        />
      </svg>
    </div>
    <p class="mb-4 text-sm text-muted-foreground">
      Redirecting into your homelab...
    </p>
    <p class="text-gray-500 mb-6">Redirecting...</p>
    <a href="/login" class="text-primary hover:underline">Go to Login</a>
  </div>
</div>
