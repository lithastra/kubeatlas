// cytoscape-dagre ships no types. The package's runtime export is a
// cytoscape Ext function (registered via cytoscape.use(...)); declaring
// the shape loosely is enough for our consumer.
declare module 'cytoscape-dagre' {
  import type { Ext } from 'cytoscape';
  const dagre: Ext;
  export default dagre;
}
