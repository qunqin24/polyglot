// Vite resolves an imported asset to its URL, and inlines a small one as a
// data URI. `vite/client` declares this too, but pulling those types in for
// one file extension would drag in the rest of them as well.
declare module "*.svg" {
  const src: string;
  export default src;
}
