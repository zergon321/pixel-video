package main

import (
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"time"
	"unsafe"

	"github.com/faiface/pixel"
	"github.com/faiface/pixel/pixelgl"
	"github.com/giorgisio/goav/avcodec"
	"github.com/giorgisio/goav/avformat"
	"github.com/giorgisio/goav/avutil"
	"github.com/giorgisio/goav/swscale"
	colors "golang.org/x/image/colornames"
)

const (
	// FrameBufferSize is the size of the frame buffer
	// to store the RGB frames from the video stream.
	FrameBufferSize = 60
	// WindowWidth is the width of the window.
	WindowWidth = 1280
	// WindowHeight is the height of the window.
	WindowHeight = 720
)

func pixToPictureData(pixels []byte, width, height int) *pixel.PictureData {
	picData := pixel.MakePictureData(pixel.
		R(0, 0, float64(width), float64(height)))

	for y := height - 1; y >= 0; y-- {
		for x := 0; x < width; x++ {
			picData.Pix[(height-y-1)*width+x].R = pixels[y*width*4+x*4+0]
			picData.Pix[(height-y-1)*width+x].G = pixels[y*width*4+x*4+1]
			picData.Pix[(height-y-1)*width+x].B = pixels[y*width*4+x*4+2]
			picData.Pix[(height-y-1)*width+x].A = pixels[y*width*4+x*4+3]
		}
	}

	return picData
}

func getFrameRGBA(frame *avutil.Frame, width, height int) *pixel.PictureData {
	pix := []byte{}

	for y := 0; y < height; y++ {
		data0 := avutil.Data(frame)[0]
		buf := make([]byte, width*4)
		startPos := uintptr(unsafe.Pointer(data0)) +
			uintptr(y)*uintptr(avutil.Linesize(frame)[0])

		for i := 0; i < width*4; i++ {
			element := *(*uint8)(unsafe.Pointer(startPos + uintptr(i)))
			buf[i] = element
		}

		pix = append(pix, buf...)
	}

	return pixToPictureData(pix, width, height)
}

func readVideoFrames(videoPath string) <-chan *pixel.PictureData {
	// Create a frame buffer.
	frameBuffer := make(chan *pixel.PictureData, FrameBufferSize)

	go func() {
		// Open a video file.
		pFormatContext := avformat.AvformatAllocContext()

		if avformat.AvformatOpenInput(&pFormatContext, videoPath, nil, nil) != 0 {
			fmt.Printf("Unable to open file %s\n", videoPath)
			os.Exit(1)
		}

		// Retrieve the stream information.
		if pFormatContext.AvformatFindStreamInfo(nil) < 0 {
			fmt.Println("Couldn't find stream information")
			os.Exit(1)
		}

		// Dump information about the video to stderr.
		pFormatContext.AvDumpFormat(0, videoPath, 0)

		// Find the first video stream
		for i := 0; i < int(pFormatContext.NbStreams()); i++ {
			switch pFormatContext.Streams()[i].
				CodecParameters().AvCodecGetType() {
			case avformat.AVMEDIA_TYPE_VIDEO:

				// Get a pointer to the codec context for the video stream
				pCodecCtxOrig := pFormatContext.Streams()[i].Codec()
				// Find the decoder for the video stream
				pCodec := avcodec.AvcodecFindDecoder(avcodec.
					CodecId(pCodecCtxOrig.GetCodecId()))

				if pCodec == nil {
					fmt.Println("Unsupported codec!")
					os.Exit(1)
				}

				// Copy context
				pCodecCtx := pCodec.AvcodecAllocContext3()

				if pCodecCtx.AvcodecCopyContext((*avcodec.
					Context)(unsafe.Pointer(pCodecCtxOrig))) != 0 {
					fmt.Println("Couldn't copy codec context")
					os.Exit(1)
				}

				// Open codec
				if pCodecCtx.AvcodecOpen2(pCodec, nil) < 0 {
					fmt.Println("Could not open codec")
					os.Exit(1)
				}

				// Allocate video frame
				pFrame := avutil.AvFrameAlloc()

				// Allocate an AVFrame structure
				pFrameRGB := avutil.AvFrameAlloc()

				if pFrameRGB == nil {
					fmt.Println("Unable to allocate RGB Frame")
					os.Exit(1)
				}

				// Determine required buffer size and allocate buffer
				numBytes := uintptr(avcodec.AvpictureGetSize(
					avcodec.AV_PIX_FMT_RGBA, pCodecCtx.Width(),
					pCodecCtx.Height()))
				buffer := avutil.AvMalloc(numBytes)

				// Assign appropriate parts of buffer to image planes in pFrameRGB
				// Note that pFrameRGB is an AVFrame, but AVFrame is a superset
				// of AVPicture
				avp := (*avcodec.Picture)(unsafe.Pointer(pFrameRGB))
				avp.AvpictureFill((*uint8)(buffer),
					avcodec.AV_PIX_FMT_RGBA, pCodecCtx.Width(), pCodecCtx.Height())

				// initialize SWS context for software scaling
				swsCtx := swscale.SwsGetcontext(
					pCodecCtx.Width(),
					pCodecCtx.Height(),
					(swscale.PixelFormat)(pCodecCtx.PixFmt()),
					pCodecCtx.Width(),
					pCodecCtx.Height(),
					avcodec.AV_PIX_FMT_RGBA,
					avcodec.SWS_BILINEAR,
					nil,
					nil,
					nil,
				)

				// Read frames and save first five frames to disk
				packet := avcodec.AvPacketAlloc()

				for pFormatContext.AvReadFrame(packet) >= 0 {
					// Is this a packet from the video stream?
					if packet.StreamIndex() == i {
						// Decode video frame
						response := pCodecCtx.AvcodecSendPacket(packet)

						if response < 0 {
							fmt.Printf("Error while sending a packet to the decoder: %s\n",
								avutil.ErrorFromCode(response))
						}

						for response >= 0 {
							response = pCodecCtx.AvcodecReceiveFrame(
								(*avcodec.Frame)(unsafe.Pointer(pFrame)))

							if response == avutil.AvErrorEAGAIN ||
								response == avutil.AvErrorEOF {
								break
							} else if response < 0 {
								fmt.Printf("Error while receiving a frame from the decoder: %s\n",
									avutil.ErrorFromCode(response))

								//return
							}

							// Convert the image from its native format to RGB
							swscale.SwsScale2(swsCtx, avutil.Data(pFrame),
								avutil.Linesize(pFrame), 0, pCodecCtx.Height(),
								avutil.Data(pFrameRGB), avutil.Linesize(pFrameRGB))

							// Save the frame to the frame buffer.
							frame := getFrameRGBA(pFrameRGB,
								pCodecCtx.Width(), pCodecCtx.Height())
							frameBuffer <- frame
						}
					}

					// Free the packet that was allocated by av_read_frame
					packet.AvFreePacket()
				}

				// Free the RGB image
				avutil.AvFree(buffer)
				avutil.AvFrameFree(pFrameRGB)

				// Free the YUV frame
				avutil.AvFrameFree(pFrame)

				// Close the codecs
				pCodecCtx.AvcodecClose()
				(*avcodec.Context)(unsafe.Pointer(pCodecCtxOrig)).AvcodecClose()

				// Close the video file
				pFormatContext.AvformatCloseInput()

				// Stop after saving frames of first video straem
				break

			default:
				fmt.Println("Didn't find a video stream")
				os.Exit(1)
			}
		}

		close(frameBuffer)
	}()

	return frameBuffer
}

func run() {
	if len(os.Args) < 2 {
		fmt.Println("Please provide a movie file")
		os.Exit(1)
	}

	// Create a new window.
	cfg := pixelgl.WindowConfig{
		Title:  "Pixel Rocks!",
		Bounds: pixel.R(0, 0, 1280, 720),
		VSync:  true,
	}
	win, err := pixelgl.NewWindow(cfg)
	handleError(err)

	videoSprite := pixel.NewSprite(nil, pixel.Rect{})
	videoTransform := pixel.IM.Moved(pixel.V(
		float64(WindowWidth)/2, float64(WindowHeight)/2))
	frameBuffer := readVideoFrames(os.Args[1])

	fps := 0
	frameCounter := 0
	perSecond := time.Tick(time.Second)

	for !win.Closed() {
		win.Clear(colors.White)

		select {
		case frame, ok := <-frameBuffer:
			if !ok {
				os.Exit(0)
			}

			frameCounter++

			if frame != nil {
				videoSprite.Set(frame, frame.Rect)
			}

		default:
		}

		videoSprite.Draw(win, videoTransform)

		win.Update()

		// Show FPS in the window title.
		fps++

		select {
		case <-perSecond:
			win.SetTitle(fmt.Sprintf("%s | FPS: %d | frameCounter: %d", cfg.Title, fps, frameCounter))
			fps = 0

		default:
		}
	}
}

func main() {
	go http.ListenAndServe("0.0.0.0:8080", nil)
	pixelgl.Run(run)
}

func handleError(err error) {
	if err != nil {
		panic(err)
	}
}
