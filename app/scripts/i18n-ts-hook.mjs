export async function resolve(specifier, context, next) {
  try {
    return await next(specifier, context);
  } catch (error) {
    if (/^\.\.?\//.test(specifier)) return next(`${specifier}.ts`, context);
    throw error;
  }
}
