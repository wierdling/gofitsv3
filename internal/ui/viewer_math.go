package ui

import (
	"math"

	"fyne.io/fyne/v2"
)

type rulerMeasurement struct {
	Start    imagePoint
	End      imagePoint
	DX       int
	DY       int
	Distance float64
}

func mapViewportPositionToImage(pos fyne.Position, scroll fyne.Position, zoom float64, width, height int, flipped bool) (imagePoint, bool) {
	if zoom <= 0 || width <= 0 || height <= 0 {
		return imagePoint{}, false
	}
	contentX := float64(pos.X + scroll.X)
	contentY := float64(pos.Y + scroll.Y)
	x := int(math.Floor(contentX / zoom))
	yDisplay := int(math.Floor(contentY / zoom))
	if x < 0 || yDisplay < 0 || x >= width || yDisplay >= height {
		return imagePoint{}, false
	}
	y := yDisplay
	if flipped {
		y = height - 1 - yDisplay
	}
	return imagePoint{X: x, Y: y}, true
}

func imagePointToCanvasPosition(point imagePoint, zoom float64, height int, flipped bool) fyne.Position {
	displayY := point.Y
	if flipped {
		displayY = height - 1 - point.Y
	}
	return fyne.NewPos(
		float32((float64(point.X)+0.5)*zoom),
		float32((float64(displayY)+0.5)*zoom),
	)
}

func measurePoints(start imagePoint, end imagePoint) rulerMeasurement {
	dx := end.X - start.X
	dy := end.Y - start.Y
	return rulerMeasurement{
		Start:    start,
		End:      end,
		DX:       dx,
		DY:       dy,
		Distance: math.Hypot(float64(dx), float64(dy)),
	}
}
