/**
 * 散点图二维缩放：用 span/offset 表示可见窗口占全域的比例。
 * 不引入 chartjs-plugin-zoom，与渠道监控的滚轮缩放同一套思路。
 */

export interface ScatterZoom {
  /** 横轴可见比例（1 = 全范围）。 */
  xSpan: number
  /** 横轴窗口起点，落在 [0, 1 - xSpan]。 */
  xOffset: number
  /** 纵轴可见比例（1 = 全范围）。 */
  ySpan: number
  /** 纵轴窗口起点（数据坐标从下往上），落在 [0, 1 - ySpan]。 */
  yOffset: number
}

export interface ScatterDomain {
  xMin: number
  xMax: number
  yMin: number
  yMax: number
}

export interface PlotArea {
  left: number
  top: number
  width: number
  height: number
}

export const DEFAULT_SCATTER_ZOOM: ScatterZoom = {
  xSpan: 1,
  xOffset: 0,
  ySpan: 1,
  yOffset: 0
}

/** 最小可见比例：约 1%，2000 刀全幅时大约 20 刀窗口。 */
export const MIN_SCATTER_ZOOM_SPAN = 0.01

const ZOOM_STEP = 0.16

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value))
}

export function resetScatterZoom(): ScatterZoom {
  return { ...DEFAULT_SCATTER_ZOOM }
}

export function isScatterZoomed(state: ScatterZoom): boolean {
  return (
    state.xSpan < 0.999 ||
    state.ySpan < 0.999 ||
    state.xOffset > 0.001 ||
    state.yOffset > 0.001
  )
}

export function clampScatterZoom(state: ScatterZoom): ScatterZoom {
  const xSpan = clamp(state.xSpan, MIN_SCATTER_ZOOM_SPAN, 1)
  const ySpan = clamp(state.ySpan, MIN_SCATTER_ZOOM_SPAN, 1)
  return {
    xSpan,
    xOffset: clamp(state.xOffset, 0, Math.max(0, 1 - xSpan)),
    ySpan,
    yOffset: clamp(state.yOffset, 0, Math.max(0, 1 - ySpan))
  }
}

function zoomAxis(span: number, offset: number, nextSpan: number, ratio: number): {
  span: number
  offset: number
} {
  const dataPos = offset + ratio * span
  return {
    span: nextSpan,
    offset: dataPos - ratio * nextSpan
  }
}

/**
 * 滚轮以光标为中心同时缩放 XY。
 * Shift / 横向滚轮只平移横轴，方便在放大后左右扫金额。
 */
export function applyScatterWheelZoom(
  state: ScatterZoom,
  event: Pick<WheelEvent, 'deltaY' | 'deltaX' | 'shiftKey'>,
  xRatio: number,
  yRatio: number
): ScatterZoom {
  const x = clamp(Number.isFinite(xRatio) ? xRatio : 0.5, 0, 1)
  const y = clamp(Number.isFinite(yRatio) ? yRatio : 0.5, 0, 1)
  const isPan = event.shiftKey || Math.abs(event.deltaX) > Math.abs(event.deltaY)

  if (isPan) {
    if (state.xSpan >= 1 && state.ySpan >= 1) return clampScatterZoom(state)
    const delta = event.shiftKey ? event.deltaY : event.deltaX
    return clampScatterZoom({
      ...state,
      xOffset: state.xOffset + (delta / 400) * state.xSpan
    })
  }

  const direction = event.deltaY < 0 ? -1 : event.deltaY > 0 ? 1 : 0
  if (direction === 0) return clampScatterZoom(state)

  const factor = 1 + direction * ZOOM_STEP
  const nextX = clamp(state.xSpan * factor, MIN_SCATTER_ZOOM_SPAN, 1)
  const nextY = clamp(state.ySpan * factor, MIN_SCATTER_ZOOM_SPAN, 1)
  if (Math.abs(nextX - state.xSpan) < 1e-9 && Math.abs(nextY - state.ySpan) < 1e-9) {
    return clampScatterZoom(state)
  }

  const xAxis = zoomAxis(state.xSpan, state.xOffset, nextX, x)
  const yAxis = zoomAxis(state.ySpan, state.yOffset, nextY, y)
  return clampScatterZoom({
    xSpan: xAxis.span,
    xOffset: xAxis.offset,
    ySpan: yAxis.span,
    yOffset: yAxis.offset
  })
}

/** 按钮放大 / 缩小：以给定比例点（默认中心）为锚。 */
export function applyScatterButtonZoom(
  state: ScatterZoom,
  direction: 'in' | 'out',
  xRatio = 0.5,
  yRatio = 0.5
): ScatterZoom {
  return applyScatterWheelZoom(
    state,
    { deltaY: direction === 'in' ? -100 : 100, deltaX: 0, shiftKey: false },
    xRatio,
    yRatio
  )
}

/**
 * 拖动平移。dxPlotRatio / dyPlotRatio 是指针位移占绘图区宽高的比例。
 * 内容跟随指针：向右拖看到更左边的金额。
 */
export function applyScatterPan(
  state: ScatterZoom,
  dxPlotRatio: number,
  dyPlotRatio: number
): ScatterZoom {
  if (state.xSpan >= 1 && state.ySpan >= 1) return clampScatterZoom(state)
  return clampScatterZoom({
    xSpan: state.xSpan,
    xOffset: state.xOffset - dxPlotRatio * state.xSpan,
    ySpan: state.ySpan,
    yOffset: state.yOffset + dyPlotRatio * state.ySpan
  })
}

export function visibleScatterDomain(domain: ScatterDomain, zoom: ScatterZoom): ScatterDomain {
  const z = clampScatterZoom(zoom)
  const xRange = Math.max(domain.xMax - domain.xMin, 1e-9)
  const yRange = Math.max(domain.yMax - domain.yMin, 1e-9)
  return {
    xMin: domain.xMin + z.xOffset * xRange,
    xMax: domain.xMin + (z.xOffset + z.xSpan) * xRange,
    yMin: domain.yMin + z.yOffset * yRange,
    yMax: domain.yMin + (z.yOffset + z.ySpan) * yRange
  }
}

/** 把事件坐标映射到绘图区 [0,1]；纵轴按数据方向（下=0，上=1）。 */
export function eventToPlotRatios(
  event: { offsetX: number; offsetY: number },
  area?: PlotArea | null
): { xRatio: number; yRatio: number } {
  if (!area || area.width <= 0 || area.height <= 0) {
    return { xRatio: 0.5, yRatio: 0.5 }
  }
  return {
    xRatio: clamp((event.offsetX - area.left) / area.width, 0, 1),
    yRatio: clamp(1 - (event.offsetY - area.top) / area.height, 0, 1)
  }
}

export function isInsidePlotArea(
  event: { offsetX: number; offsetY: number },
  area?: PlotArea | null
): boolean {
  if (!area) return true
  return (
    event.offsetX >= area.left &&
    event.offsetX <= area.left + area.width &&
    event.offsetY >= area.top &&
    event.offsetY <= area.top + area.height
  )
}
