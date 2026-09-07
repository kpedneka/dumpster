import { useEffect, useMemo, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import cytoscape, { type Core, type ElementDefinition } from 'cytoscape'
import fcose from 'cytoscape-fcose'
import { Circle, Clock, Diamond, Hexagon, Square, Star, Triangle, ZoomOut, type LucideIcon } from 'lucide-react'
import { api } from '@/api/client'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

// fcose (not core Cytoscape's built-in cose) specifically because it treats
// compound nodes as first-class citizens of the layout, not an afterthought
// -- base cose was leaving community boxes overlapping each other by
// default, which compounded the "hard to click a box" problem below since
// an overlapping box steals another box's clicks. Registered once at module
// load, not per-render.
cytoscape.use(fcose)

type GraphNode = {
  id: string
  label: string
  type: string
  community_id: number | null
  degree: number
  document_count: number
}
type GraphEdge = {
  source: string
  target: string
  weight: number
  document_count: number
  relation_type: string | null
}

// SHAPE_BY_TYPE gives entity_type its own visual channel, independent of
// the community-driven fill color -- color already encodes "which
// community," so reusing color for "what kind of thing" would mean
// decoding two different categorical scales through the same channel.
// Shape has no such conflict. Falls back to 'ellipse' for any type outside
// this list, since ENTITY_TYPES is operator-configurable (see
// internal/config) and can carry types this list doesn't know about.
const SHAPE_BY_TYPE: Record<string, cytoscape.Css.NodeShape> = {
  person: 'ellipse',
  organization: 'rectangle',
  location: 'triangle',
  concept: 'diamond',
  event: 'star',
  work_of_art: 'hexagon',
  date_time: 'round-rectangle',
}
function shapeFor(type: string): cytoscape.Css.NodeShape {
  return SHAPE_BY_TYPE[type] ?? 'ellipse'
}

// LEGEND_ICON_BY_TYPE mirrors SHAPE_BY_TYPE with a matching Lucide icon for
// the on-screen legend -- kept as a separate table (rather than deriving
// one from the other) since a legend needs something a person recognizes
// as "circle"/"star"/etc., not literally the same string cytoscape's shape
// property expects.
const LEGEND_ICON_BY_TYPE: Record<string, LucideIcon> = {
  person: Circle,
  organization: Square,
  location: Triangle,
  concept: Diamond,
  event: Star,
  work_of_art: Hexagon,
  date_time: Clock,
}
const LEGEND_TYPE_ORDER = ['person', 'organization', 'location', 'concept', 'event', 'work_of_art', 'date_time']

// CROSS_SOURCE_COLOR marks an entity or edge backed by more than one
// document -- a direct, visible answer to "how do topics from different
// sources in this KB overlap": a plain node/edge only ever came from one
// document; a highlighted one is a real bridge between sources.
const CROSS_SOURCE_COLOR = '#d97706' // amber-600, distinct from every community palette hue

// COMMUNITY_PALETTE cycles by community_id % length -- a fixed, small set
// rather than generating colors, so two adjacent communities never land on
// visually similar hues by chance the way an HSL-rotation-by-id scheme can.
// Each entry pairs a compound-node "area" fill with a deeper node fill from
// the same hue family, so a community reads as one visual group at a
// glance before any label is ever shown.
const COMMUNITY_PALETTE = [
  { area: '#dbeafe', node: '#3b82f6' }, // blue
  { area: '#dcfce7', node: '#22c55e' }, // green
  { area: '#fce7f3', node: '#ec4899' }, // pink
  { area: '#fef3c7', node: '#f59e0b' }, // amber
  { area: '#ede9fe', node: '#8b5cf6' }, // violet
  { area: '#ffe4e6', node: '#f43f5e' }, // rose
  { area: '#cffafe', node: '#06b6d4' }, // cyan
  { area: '#dcfce7', node: '#84cc16' }, // lime (repeats green family intentionally past 7 communities)
]
const NO_COMMUNITY_COLOR = '#94a3b8' // neutral slate for entities with no community yet

function colorFor(communityId: number | null): { area: string; node: string } {
  if (communityId === null) return { area: '#f1f5f9', node: NO_COMMUNITY_COLOR }
  return COMMUNITY_PALETTE[communityId % COMMUNITY_PALETTE.length]
}

// GraphPanel is a prototype for the Graph Visualization card: communities
// render as compound "area" containers (Cytoscape's native grouping
// primitive -- chosen specifically because it draws a real bounded region,
// not just node color, for "highlight the area a community occupies"),
// clicking one fits the viewport to just that subset ("dive deeper"),
// clicking the background resets to the full graph, and labels stay
// hidden until a node/edge is clicked -- otherwise a graph this dense
// reads as pure noise. The backend already caps each community to its
// top few nodes by degree (see filterGraphView server-side) -- this
// component doesn't do any filtering of its own, just renders whatever
// it's sent. Communities with a computed Theme get that as their label
// instead of a bare number; most won't (Theme Summarization only labels
// the largest handful), so "Community N" is a real, expected fallback,
// not a bug. This is intentionally rough around the edges (fixed
// palette, no persisted layout) -- the point is to see the interaction
// model before investing further.
export function GraphPanel({ kbId }: { kbId: string }) {
  const containerRef = useRef<HTMLDivElement>(null)
  const cyRef = useRef<Core | null>(null)
  const [focusedCommunity, setFocusedCommunity] = useState<number | null>(null)

  const { data, isLoading, error } = useQuery({
    queryKey: ['graph', kbId],
    queryFn: async () => {
      const { data, error } = await api.GET('/kbs/{id}/graph', { params: { path: { id: kbId } } })
      if (error) throw error
      return data
    },
  })

  // Same query key ExploreSection's Themes panel uses -- a free cache hit
  // if Themes has already been opened/computed, a normal (cheap) GET
  // otherwise. Themes only labels the largest handful of communities (see
  // ThemeSummarization's own scope), so most communities fall back to a
  // plain "Community N" -- real, just not everything has a topic yet.
  const { data: themeResult } = useQuery({
    queryKey: ['themes', kbId],
    queryFn: async () => {
      const { data, error } = await api.GET('/kbs/{id}/themes', { params: { path: { id: kbId } } })
      if (error) throw error
      return data
    },
  })
  const themeLabelByCommunity = useMemo(() => {
    const map = new Map<number, string>()
    for (const t of themeResult?.themes ?? []) map.set(t.community_id, t.label)
    return map
  }, [themeResult])

  const elements = useMemo<ElementDefinition[]>(() => {
    if (!data) return []
    const nodes = data.nodes as GraphNode[]
    const edges = data.edges as GraphEdge[]

    const communityIds = new Set<number>()
    for (const n of nodes) if (n.community_id !== null) communityIds.add(n.community_id)

    const els: ElementDefinition[] = []
    for (const cid of communityIds) {
      const members = nodes.filter((n) => n.community_id === cid)
      const topic = themeLabelByCommunity.get(cid)
      // Theme Summarization only writes a real topic for the handful of
      // communities it judges genuinely distinctive (see internal/theme's
      // SelectTopCommunities + Summarize) -- most communities never get
      // one. Rather than leave those as a bare "Community N" (meaningless
      // to a reader), fall back to its own top few entities by degree as a
      // free, no-LLM-call label -- the same signal the node sizing already
      // uses, just surfaced as text.
      const fallback = [...members]
        .sort((a, b) => b.degree - a.degree)
        .slice(0, 3)
        .map((n) => n.label)
        .join(', ')
      els.push({
        data: { id: `community-${cid}`, label: `${topic ?? fallback} (${members.length})`, isCommunity: true },
        classes: 'community',
      })
    }
    for (const n of nodes) {
      els.push({
        data: {
          id: n.id,
          label: n.label,
          type: n.type,
          communityId: n.community_id,
          documentCount: n.document_count,
          parent: n.community_id !== null ? `community-${n.community_id}` : undefined,
        },
      })
    }
    for (const e of edges) {
      els.push({
        data: {
          id: `${e.source}-${e.target}`,
          source: e.source,
          target: e.target,
          weight: e.weight,
          documentCount: e.document_count,
          // Only ever a real relationship label ("founded", "works at") or
          // absent -- relation.NoneRelation ("no relationship found") is
          // filtered out server-side before this ever reaches the graph
          // response, so there's nothing here worth distinguishing from
          // "not yet reviewed."
          relationType: e.relation_type,
        },
      })
    }
    return els
  }, [data, themeLabelByCommunity])

  // Legend only shows types actually present in this KB's graph (an
  // operator can add/remove types via ENTITY_TYPES, so the fixed 7-type
  // table above isn't guaranteed to match reality) and only mentions
  // cross-source highlighting at all when something in this graph actually
  // qualifies -- a single-document KB has nothing to explain there.
  const typesPresent = useMemo(() => {
    const seen = new Set<string>()
    for (const n of data?.nodes ?? []) seen.add(n.type)
    return LEGEND_TYPE_ORDER.filter((t) => seen.has(t))
  }, [data])
  const hasCrossSource = useMemo(
    () => (data?.nodes ?? []).some((n) => n.document_count > 1) || (data?.edges ?? []).some((e) => e.document_count > 1),
    [data],
  )

  useEffect(() => {
    if (!containerRef.current || elements.length === 0) return

    const cy = cytoscape({
      container: containerRef.current,
      elements,
      // Labels are hidden by default (empty content) at every element
      // class below -- ".revealed" (toggled on tap) is the only selector
      // that ever sets real label text. See the tap handler for why.
      style: [
        {
          selector: 'node',
          style: {
            'background-color': (ele) => colorFor(ele.data('communityId') ?? null).node,
            // Shape is entity_type's own visual channel -- see SHAPE_BY_TYPE
            // -- independent of the community-driven fill color above.
            shape: (ele) => shapeFor(String(ele.data('type'))),
            width: (ele: cytoscape.NodeSingular) => 14 + Math.min(ele.degree(), 10) * 2,
            height: (ele: cytoscape.NodeSingular) => 14 + Math.min(ele.degree(), 10) * 2,
            // A node more than one document mentions gets a thick amber
            // ring instead of the default thin white one -- the answer to
            // "how do topics from different sources overlap": a plain node
            // only ever came from one document, a ringed one is a real
            // cross-document bridge.
            'border-width': (ele) => (Number(ele.data('documentCount')) > 1 ? 3 : 1),
            'border-color': (ele) => (Number(ele.data('documentCount')) > 1 ? CROSS_SOURCE_COLOR : '#ffffff'),
            // Entity labels on by default, per explicit request to try this
            // out -- a departure from the original "only on click" design,
            // which was there specifically to reduce noise on a much larger
            // unfiltered graph. Edges keep the click-to-reveal behavior
            // below: there are usually far more of them than nodes, and an
            // edge's weight is far less useful to see at a glance than an
            // entity's name is.
            content: 'data(label)',
            'font-size': 9,
            'text-valign': 'bottom',
            'text-margin-y': 4,
          },
        },
        {
          selector: 'node.community',
          style: {
            'background-color': (ele) => {
              const cid = Number(String(ele.data('id')).replace('community-', ''))
              return colorFor(cid).area
            },
            'background-opacity': 0.5,
            'border-width': 2,
            'border-style': 'dashed',
            'border-color': (ele) => {
              const cid = Number(String(ele.data('id')).replace('community-', ''))
              return colorFor(cid).node
            },
            content: 'data(label)',
            'text-valign': 'top',
            'text-halign': 'center',
            'font-size': 11,
            'font-weight': 600,
            color: '#334155',
            padding: '24px',
          },
        },
        {
          selector: 'edge',
          style: {
            // Floor raised from ~1px to 2.5px -- a thin low-weight edge was
            // effectively unclickable except at heavy zoom, since
            // Cytoscape's hit-test tolerance for an edge scales with its
            // rendered width. Still capped at 6 so a handful of
            // heavily-weighted edges don't dominate the view.
            width: (ele: cytoscape.EdgeSingular) => Math.min(Math.max(2.5, 1 + Math.log2(1 + ele.data('weight'))), 6),
            // An edge more than one document contributed to is a
            // cross-document connection, not just an incidental pairing
            // inside one source -- highlighted the same way a
            // multi-document node is, so a bridge reads consistently
            // whether you're looking at the node or the edge that forms it.
            'line-color': (ele) => (Number(ele.data('documentCount')) > 1 ? CROSS_SOURCE_COLOR : '#cbd5e1'),
            'line-cap': 'round',
            'curve-style': 'haystack',
            content: '',
          },
        },
        {
          selector: 'edge.revealed',
          style: {
            // A real relationship label ("founded", "works at") is what
            // this whole graph was missing -- show it when
            // internal/relation has actually reviewed this pair. Falls
            // back to the raw weight for the (likely majority, until a KB
            // has been run through relation extraction) edges that
            // haven't been reviewed yet.
            // Cytoscape's type defs constrain every 'content' mapper to
            // NodeSingular regardless of selector -- .data() exists on both
            // node and edge singulars at runtime, so this is safe despite
            // the annotation mismatch an explicit EdgeSingular type would
            // otherwise hit.
            content: (ele) => (ele.data('relationType') as string | null) ?? String(ele.data('weight')),
            'font-size': 8,
            color: '#64748b',
            'text-background-color': '#fff',
            'text-background-opacity': 0.8,
          },
        },
        {
          selector: '.dimmed',
          style: { opacity: 0.15 },
        },
      ],
      layout: {
        name: 'fcose',
        animate: false,
        nodeDimensionsIncludeLabels: true,
        // packComponents spaces out disconnected pieces of the graph (e.g.
        // separate communities with no edge between them) as distinct
        // clusters instead of letting fcose's force simulation leave them
        // wherever they happen to settle, which was producing the
        // overlapping community boxes reported as hard to undo.
        packComponents: true,
        nodeRepulsion: 8000,
        idealEdgeLength: 80,
        nodeSeparation: 75,
      } as cytoscape.LayoutOptions,
      minZoom: 0.1,
      maxZoom: 4,
      // Cytoscape's default renderer already handles touch gestures
      // (pinch-zoom, tap, pan) with no extra config -- nothing mobile-
      // specific needed here.
      wheelSensitivity: 0.2,
    })
    cyRef.current = cy

    // Tap an edge: reveal its weight label (edges keep click-to-reveal --
    // node labels are unconditional now, see the base 'node' style above).
    // Tap a node: no label state to toggle any more, just center it.
    cy.on('tap', 'node, edge', (evt) => {
      if (evt.target.hasClass('community')) return // community taps are handled by the dedicated handler below
      if (evt.target.isEdge()) evt.target.toggleClass('revealed')
      // The label a reveal just turned on can land outside the current
      // viewport -- most visibly for an edge, which often only becomes
      // clickable at a zoom level tight enough that its midpoint (where the
      // label renders) has already scrolled off-screen. Center on whatever
      // was just tapped so the label it revealed (or just the node itself)
      // is actually visible.
      if (evt.target.isNode() || evt.target.hasClass('revealed')) {
        cy.animate({ center: { eles: evt.target }, duration: 200 })
      }
    })

    // Tap a community's compound container: "dive deeper" -- fit the
    // viewport to just that community's nodes and dim everything else.
    cy.on('tap', 'node.community', (evt) => {
      const communityNode = evt.target
      const cid = Number(String(communityNode.data('id')).replace('community-', ''))
      const subset = communityNode.union(communityNode.children())
      cy.elements().addClass('dimmed')
      subset.removeClass('dimmed')
      // Bring the focused community's box above every edge -- by default
      // edges are the last-inserted elements and win ties in Cytoscape's
      // draw/hit-test order, which is exactly why the box was hard to tap
      // in the first place. Reset on zoom-out below so unfocused edges
      // stay normally clickable rather than permanently losing to boxes.
      cy.nodes('.community').style('z-index', 0)
      communityNode.style('z-index', 999)
      cy.animate({ fit: { eles: subset, padding: 40 }, duration: 400 })
      setFocusedCommunity(cid)
    })

    // Tap empty background: "zoom out" back to the full graph.
    cy.on('tap', (evt) => {
      if (evt.target !== cy) return
      cy.elements().removeClass('dimmed')
      cy.nodes('.community').style('z-index', 0)
      cy.animate({ fit: { eles: cy.elements(), padding: 30 }, duration: 400 })
      setFocusedCommunity(null)
    })

    return () => {
      cy.destroy()
      cyRef.current = null
    }
  }, [elements])

  function resetView() {
    const cy = cyRef.current
    if (!cy) return
    cy.elements().removeClass('dimmed')
    cy.nodes('.community').style('z-index', 0)
    cy.animate({ fit: { eles: cy.elements(), padding: 30 }, duration: 400 })
    setFocusedCommunity(null)
  }

  if (isLoading) return <p className="text-xs">Loading graph…</p>
  if (error) return <p className="text-xs">Failed to load the graph.</p>
  if (!data || data.nodes.length === 0) {
    return (
      <p className="text-xs">
        No entities yet. Upload documents and run entity extraction, then compute communities above to see them
        grouped here.
      </p>
    )
  }

  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center justify-between">
        <p className="text-xs opacity-80">
          {focusedCommunity !== null
            ? `Viewing ${themeLabelByCommunity.get(focusedCommunity) ?? `community ${focusedCommunity}`} — click the background to zoom back out.`
            : 'Click a community to zoom in. Click any entity or connection to see its label.'}
        </p>
        {focusedCommunity !== null && (
          <Button type="button" variant="outline" size="sm" className="bg-transparent" onClick={resetView}>
            <ZoomOut className="h-3.5 w-3.5" />
            Zoom out
          </Button>
        )}
      </div>
      <div ref={containerRef} className={cn('h-96 w-full rounded-md border border-current/20 bg-white/40')} />
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs opacity-70">
        {typesPresent.map((t) => {
          const Icon = LEGEND_ICON_BY_TYPE[t]
          return (
            <span key={t} className="flex items-center gap-1">
              <Icon className="h-3 w-3" />
              {t.replace('_', ' ')}
            </span>
          )
        })}
        {hasCrossSource && (
          <span className="flex items-center gap-1">
            <span
              className="inline-block h-2.5 w-2.5 rounded-full border-2"
              style={{ borderColor: CROSS_SOURCE_COLOR }}
            />
            appears in multiple documents
          </span>
        )}
      </div>
    </div>
  )
}
