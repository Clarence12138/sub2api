import { describe, expect, it } from 'vitest'
import {
  DEFAULT_SCATTER_ZOOM,
  MIN_SCATTER_ZOOM_SPAN,
  applyScatterButtonZoom,
  applyScatterPan,
  applyScatterWheelZoom,
  clampScatterZoom,
  eventToPlotRatios,
  isScatterZoomed,
  resetScatterZoom,
  visibleScatterDomain
} from '../scatterZoom'

describe('scatterZoom', () => {
  it('resets to the full domain', () => {
    expect(resetScatterZoom()).toEqual(DEFAULT_SCATTER_ZOOM)
    expect(isScatterZoomed(DEFAULT_SCATTER_ZOOM)).toBe(false)
  })

  it('zooms in around the cursor instead of the origin', () => {
    const aroundCost = applyScatterWheelZoom(
      DEFAULT_SCATTER_ZOOM,
      { deltaY: -100, deltaX: 0, shiftKey: false },
      0.45,
      0.2
    )
    expect(aroundCost.xSpan).toBeLessThan(1)
    expect(aroundCost.ySpan).toBeLessThan(1)
    expect(isScatterZoomed(aroundCost)).toBe(true)

    const domain = visibleScatterDomain({ xMin: 0, xMax: 1000, yMin: 0, yMax: 100 }, aroundCost)
    expect(domain.xMin).toBeLessThan(450)
    expect(domain.xMax).toBeGreaterThan(450)
    expect(domain.yMin).toBeLessThan(20)
    expect(domain.yMax).toBeGreaterThan(20)
  })

  it('keeps the left edge pinned when zooming at x=0', () => {
    const left = applyScatterWheelZoom(
      DEFAULT_SCATTER_ZOOM,
      { deltaY: -100, deltaX: 0, shiftKey: false },
      0,
      0.5
    )
    expect(left.xOffset).toBeLessThanOrEqual(0.001)
  })

  it('pans X with shift+wheel after zooming', () => {
    const zoomed = clampScatterZoom({ xSpan: 0.2, xOffset: 0.3, ySpan: 0.2, yOffset: 0.1 })
    const panned = applyScatterWheelZoom(zoomed, { deltaY: 200, deltaX: 0, shiftKey: true }, 0.5, 0.5)
    expect(panned.xSpan).toBeCloseTo(zoomed.xSpan, 5)
    expect(panned.ySpan).toBeCloseTo(zoomed.ySpan, 5)
    expect(panned.xOffset).toBeGreaterThan(zoomed.xOffset)
  })

  it('button zoom in/out changes span around center', () => {
    const zoomed = applyScatterButtonZoom(DEFAULT_SCATTER_ZOOM, 'in')
    expect(zoomed.xSpan).toBeLessThan(1)
    const restored = applyScatterButtonZoom(zoomed, 'out')
    expect(restored.xSpan).toBeGreaterThan(zoomed.xSpan)
  })

  it('drag pan follows the pointer', () => {
    const zoomed = clampScatterZoom({ xSpan: 0.2, xOffset: 0.4, ySpan: 0.2, yOffset: 0.3 })
    const panned = applyScatterPan(zoomed, 0.25, 0)
    expect(panned.xOffset).toBeLessThan(zoomed.xOffset)
    expect(panned.yOffset).toBeCloseTo(zoomed.yOffset, 5)
  })

  it('clamps to the minimum span and stays inside the domain', () => {
    const tight = clampScatterZoom({ xSpan: 0.001, xOffset: 0.99, ySpan: 0.001, yOffset: 0.99 })
    expect(tight.xSpan).toBe(MIN_SCATTER_ZOOM_SPAN)
    expect(tight.ySpan).toBe(MIN_SCATTER_ZOOM_SPAN)
    expect(tight.xOffset + tight.xSpan).toBeLessThanOrEqual(1.001)
    expect(tight.yOffset + tight.ySpan).toBeLessThanOrEqual(1.001)
  })

  it('maps pointer position onto the plot with inverted Y', () => {
    const ratios = eventToPlotRatios(
      { offsetX: 150, offsetY: 40 },
      { left: 100, top: 20, width: 200, height: 80 }
    )
    expect(ratios.xRatio).toBeCloseTo(0.25, 5)
    expect(ratios.yRatio).toBeCloseTo(0.75, 5)
  })
})
