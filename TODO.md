In the "Mosaic" screen I need to make the following updates.
  1.  The left pane needs to have 20px padding or margin (I am not sure what is correct here, please let me know so I will use the correct terminology in the future) to the left and right.
  2.  For the image section we need to move the black/zoom/white section to below the image.
  3.  For the image section we need to mvoe the histogram to below the Mean/Std (but above the image).
  4.  For the image section, align the Mean/Std to the right.
  5.  Move the "Save Drizzle Fits" button and "Send to Examine" to the section with the Mean and Std (they should go to the right of those items).
  6.  For the buttons in the left hand side, I would like to have more padding between the buttons.
  7.  For the buttons in the left hand side, I would like the buttons to have a larger corner radius.
  8.  For the buttons in the left hand side, the space between the rows of buttons needs to be the same.  Set it at 15 px for now.
  9.  Turn the "Save Preview" checkbox into our toggle control.
  10. In the left hand side, use 15 px spacing between the Mode, background, peak, and scaled peak rows.
  11. For the buttons in the left hand side, put the "Clear offsets" and "Clear" buttons on the same row.
 

Please make the following updates:
  1.  please add 20 px padding below the "Scaled Peak" row.  
  2.  the padding is not the same for the 5 rows that are just buttons (first button is "set reference baseline..." last button is "clear").  
  3.  The "Input frames" section headers are not aligned with the rows of frames.
  4.  The left hand pane should have 20 px padding between the right side of the container and the scrollbar.  Right now when the scrollbar activates it hides the right edges of the controls.
  5.  The "Input frames" scrollbar is hidden by the full sized scrollbar and is hard to get to.  
  6.  Can we change the bottom two parts "Input Frames" and "Input Status" into a tab control?
  7.  Add 20 px padding below the right hand row that has the "black/zoom/white" controls.
  8.  
go build -ldflags="-H windowsgui -s -w" -o dist\GoFitsV3.exe  ./cmd/...

Please make the following updates to the examine workspace (workspace_examine.go)
  1.  Move the "Load current fits" and "Load fits" to the same line.
  2.  Add right and left padding to the left hand part of the screen (it has "Load fits" for the first button and "Send to channel" for the last button).
  3.  Turn the "Show clipped" and "Flip image vertically" checkboxe into the toggle buttons.
  4.  Align the "Mean" and "Std" button to the right.
  

