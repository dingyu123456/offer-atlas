package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"

	"github.com/leaanthony/winicon"
)

const (
	iconSize       = 512
	samplesPerAxis = 4
)

var (
	teal  = color.RGBA{R: 14, G: 107, B: 98, A: 255}
	white = color.RGBA{R: 255, G: 255, B: 255, A: 255}
)

func main() {
	imageData := renderIcon()
	pngData, err := encodePNG(imageData)
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join("build", "appicon.png"), pngData, 0o644); err != nil {
		panic(err)
	}

	icoFile, err := os.Create(filepath.Join("build", "windows", "icon.ico"))
	if err != nil {
		panic(err)
	}
	defer icoFile.Close()
	if err := winicon.GenerateIcon(bytes.NewReader(pngData), icoFile, []int{256, 128, 64, 48, 32, 16}); err != nil {
		panic(err)
	}
	fmt.Println("Generated transparent Windows icon assets.")
}

func renderIcon() image.Image {
	result := image.NewRGBA(image.Rect(0, 0, iconSize, iconSize))
	for y := 0; y < iconSize; y++ {
		for x := 0; x < iconSize; x++ {
			var red, green, blue, alpha int
			for sampleY := 0; sampleY < samplesPerAxis; sampleY++ {
				for sampleX := 0; sampleX < samplesPerAxis; sampleX++ {
					pointX := float64(x) + (float64(sampleX)+.5)/samplesPerAxis
					pointY := float64(y) + (float64(sampleY)+.5)/samplesPerAxis
					pixel, visible := iconColorAt(pointX, pointY)
					if visible {
						red += int(pixel.R)
						green += int(pixel.G)
						blue += int(pixel.B)
						alpha += int(pixel.A)
					}
				}
			}
			count := samplesPerAxis * samplesPerAxis
			result.SetRGBA(x, y, color.RGBA{R: uint8(red / count), G: uint8(green / count), B: uint8(blue / count), A: uint8(alpha / count)})
		}
	}
	return result
}

func iconColorAt(x, y float64) (color.RGBA, bool) {
	if !insideRoundedSquare(x, y) {
		return color.RGBA{}, false
	}
	result := teal
	distance := math.Hypot(x-256, y-256)
	if distance <= 186 && distance >= 154 {
		result = white
	}
	if insideCompassMark(x, y) {
		result = white
	}
	return result, true
}

func insideRoundedSquare(x, y float64) bool {
	dx := math.Max(math.Abs(x-256)-140, 0)
	dy := math.Max(math.Abs(y-256)-140, 0)
	return dx*dx+dy*dy <= 116*116
}

func insideCompassMark(x, y float64) bool {
	points := [][2]float64{
		{323.84, 188.16},
		{294.976, 274.736},
	}
	points = append(points, circularArc([2]float64{262.976, 262.976}, 32, math.Atan2(11.76, 32), math.Atan2(32, 11.76), 8)...)
	points = append(points,
		[2]float64{188.16, 323.84},
		[2]float64{217.024, 237.264},
	)
	points = append(points, circularArc([2]float64{249.024, 249.024}, 32, math.Atan2(-11.76, -32), math.Atan2(-32, -11.76), 8)...)
	points = append(points, [2]float64{323.84, 188.16})
	for index := 0; index < len(points)-1; index++ {
		if distanceToSegment(x, y, points[index], points[index+1]) <= 16 {
			return true
		}
	}
	return false
}

func circularArc(center [2]float64, radius, from, to float64, segments int) [][2]float64 {
	points := make([][2]float64, 0, segments)
	for index := 1; index <= segments; index++ {
		angle := from + (to-from)*float64(index)/float64(segments)
		points = append(points, [2]float64{center[0] + radius*math.Cos(angle), center[1] + radius*math.Sin(angle)})
	}
	return points
}

func distanceToSegment(x, y float64, start, end [2]float64) float64 {
	deltaX, deltaY := end[0]-start[0], end[1]-start[1]
	projection := ((x-start[0])*deltaX + (y-start[1])*deltaY) / (deltaX*deltaX + deltaY*deltaY)
	projection = math.Max(0, math.Min(1, projection))
	return math.Hypot(x-(start[0]+projection*deltaX), y-(start[1]+projection*deltaY))
}

func encodePNG(imageData image.Image) ([]byte, error) {
	buffer := bytes.NewBuffer(nil)
	if err := png.Encode(buffer, imageData); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}
