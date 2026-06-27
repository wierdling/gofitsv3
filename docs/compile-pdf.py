import subprocess
import os
import sys

def main():
    print("Starting PDF compilation process...")
    
    # 1. Paths configuration
    docs_dir = os.path.dirname(os.path.abspath(__file__))
    md_path = os.path.join(docs_dir, "user-guide.md")
    html_path = os.path.join(docs_dir, "user-guide.html")
    pdf_path = os.path.join(docs_dir, "user-guide.pdf")
    edge_path = r"C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe"
    
    if not os.path.exists(md_path):
        print(f"Error: Markdown file not found at {md_path}")
        sys.exit(1)
        
    print(f"Source markdown: {md_path}")
    print(f"Target HTML: {html_path}")
    print(f"Target PDF: {pdf_path}")
    
    # 2. Run Pandoc to generate raw HTML
    print("Running pandoc to convert Markdown to HTML...")
    try:
        # Generate a standalone HTML file (-s)
        subprocess.run(
            ["pandoc", "-s", "-f", "markdown", "-t", "html", "-o", html_path, md_path],
            check=True
        )
        print("Pandoc HTML generation successful.")
    except subprocess.CalledProcessError as e:
        print(f"Error executing pandoc: {e}")
        sys.exit(1)
    except FileNotFoundError:
        print("Error: pandoc.exe is not installed or not in PATH.")
        sys.exit(1)
        
    # 3. Read HTML and inject beautiful print-oriented CSS styles
    print("Injecting custom CSS and formatting into HTML...")
    
    css_styles = """
    @import url('https://fonts.googleapis.com/css2?family=Inter:wght@300;400;500;600;700&family=JetBrains+Mono:wght@400;500&display=swap');
    
    body {
        font-family: 'Inter', -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif;
        color: #2D3748;
        line-height: 1.65;
        font-size: 11pt;
        margin: 0;
        padding: 0;
    }
    
    @page {
        size: letter;
        margin: 2.5cm 2.2cm;
        @bottom-right {
            content: counter(page);
            font-size: 9pt;
            color: #718096;
        }
    }
    
    /* Document header & structure formatting */
    .container {
        max-width: 800px;
        margin: 0 auto;
    }
    
    h1, h2, h3, h4 {
        font-family: 'Inter', sans-serif;
        font-weight: 700;
        color: #1A365D; /* Deep Navy Blue */
        page-break-after: avoid;
        break-after: avoid;
    }
    
    h1 {
        font-size: 28pt;
        line-height: 1.2;
        margin-top: 0;
        margin-bottom: 24px;
        color: #1A365D;
        border-bottom: 3px solid #3182CE;
        padding-bottom: 12px;
    }
    
    h2 {
        font-size: 18pt;
        margin-top: 40px;
        margin-bottom: 16px;
        border-bottom: 1px solid #E2E8F0;
        padding-bottom: 8px;
        color: #2C5282;
        page-break-before: always; /* Start major sections on new page */
        break-before: page;
    }
    
    /* Prevent first heading from breaking page */
    #gofitsv3-a-beginners-guide-to-astronomical-fits-image-processing {
        page-break-before: avoid !important;
        break-before: avoid !important;
    }
    
    h3 {
        font-size: 13pt;
        margin-top: 24px;
        margin-bottom: 8px;
        color: #2B6CB0;
    }
    
    p {
        margin-top: 0;
        margin-bottom: 16px;
        text-align: justify;
    }
    
    ul, ol {
        margin-top: 0;
        margin-bottom: 16px;
        padding-left: 24px;
    }
    
    li {
        margin-bottom: 6px;
    }
    
    /* Code and pre-formatted text styling */
    code {
        font-family: 'JetBrains Mono', 'Fira Code', Consolas, Monaco, monospace;
        font-size: 9.5pt;
        background-color: #F7FAFC;
        color: #C53030; /* Crimson for inline code */
        padding: 2px 5px;
        border-radius: 4px;
        border: 1px solid #EDF2F7;
    }
    
    pre {
        background-color: #1A202C; /* Sleek dark theme for code blocks */
        color: #EDF2F7;
        padding: 16px;
        border-radius: 8px;
        overflow-x: auto;
        margin-top: 12px;
        margin-bottom: 18px;
        border: 1px solid #2D3748;
        page-break-inside: avoid;
        break-inside: avoid;
    }
    
    pre code {
        background-color: transparent;
        color: inherit;
        padding: 0;
        border: none;
        font-size: 9.2pt;
    }
    
    /* Blockquotes representing alerts/tips */
    blockquote {
        border-left: 4px solid #3182CE;
        background-color: #EBF8FF;
        padding: 12px 18px;
        margin: 20px 0;
        border-radius: 0 6px 6px 0;
        color: #2B6CB0;
        page-break-inside: avoid;
        break-inside: avoid;
    }
    
    blockquote p {
        margin: 0;
        font-style: italic;
    }
    
    hr {
        border: 0;
        height: 1px;
        background: #E2E8F0;
        margin: 30px 0;
    }
    
    /* Images */
    img {
        max-width: 100%;
        height: auto;
        display: block;
        margin: 24px auto;
        border-radius: 6px;
        border: 1px solid #E2E8F0;
        box-shadow: 0 4px 6px -1px rgba(0, 0, 0, 0.1), 0 2px 4px -1px rgba(0, 0, 0, 0.06);
        page-break-inside: avoid;
        break-inside: avoid;
    }
    
    /* Tables */
    table {
        width: 100%;
        border-collapse: collapse;
        margin-top: 16px;
        margin-bottom: 20px;
        font-size: 10pt;
        page-break-inside: avoid;
        break-inside: avoid;
    }
    
    th, td {
        padding: 10px 14px;
        text-align: left;
        border-bottom: 1px solid #E2E8F0;
    }
    
    th {
        background-color: #EDF2F7;
        color: #2D3748;
        font-weight: 600;
        border-top: 1px solid #E2E8F0;
    }
    
    tr:nth-child(even) {
        background-color: #F7FAFC;
    }
    
    /* Custom cover/title page styling */
    .cover-page {
        height: 100vh;
        display: flex;
        flex-direction: column;
        justify-content: center;
        page-break-after: always;
        break-after: page;
        padding-top: 40px;
    }
    
    .cover-title {
        font-size: 34pt;
        color: #1A365D;
        line-height: 1.15;
        font-weight: 800;
        margin-bottom: 10px;
        border: none;
        padding: 0;
    }
    
    .cover-subtitle {
        font-size: 16pt;
        color: #4A5568;
        margin-bottom: 30px;
        font-weight: 300;
    }
    
    .cover-image {
        max-width: 100%;
        max-height: 420px;
        object-fit: cover;
        margin: 0 auto;
        border-radius: 8px;
        border: 1px solid #E2E8F0;
        box-shadow: 0 10px 15px -3px rgba(0, 0, 0, 0.1), 0 4px 6px -2px rgba(0, 0, 0, 0.05);
    }
    
    .cover-meta {
        margin-top: auto;
        font-size: 11pt;
        color: #718096;
        border-top: 2px solid #E2E8F0;
        padding-top: 20px;
        line-height: 1.8;
    }
    
    .cover-meta strong {
        color: #2D3748;
    }
    """
    
    with open(html_path, "r", encoding="utf-8") as f:
        html_content = f.read()
        
    # We want to insert the styles into the <head> block
    style_tag = f"<style>{css_styles}</style>"
    if "</head>" in html_content:
        html_content = html_content.replace("</head>", f"{style_tag}\n</head>")
    else:
        # Fallback if no head tag exists
        html_content = f"<html><head>{style_tag}</head><body>{html_content}</body></html>"
        
    # Let's wrap the body content in a div container for padding/max-width controls
    # and prepend a cover page!
    cover_html = """
    <div class="container">
    <div class="cover-page">
        <h1 class="cover-title">GoFitsV3</h1>
        <div class="cover-subtitle">A Beginner's Guide to Astronomical FITS Image Processing</div>
        
        <img src="images/M16_MTF_WFC3.png" class="cover-image" alt="M16 Pillars of Creation">
        
        <div style="flex-grow: 1;"></div>
        
        <div class="cover-meta">
            <strong>Target Audience:</strong> Astronomy Enthusiasts & Beginners<br>
            <strong>Application:</strong> GoFitsV3 Desktop FITS Processor<br>
            <strong>Data Source Reference:</strong> MAST Hubble Search Archive (<a href="https://mast.stsci.edu/search/ui/#/hst">mast.stsci.edu</a>)<br>
            <strong>Version:</strong> 1.0 (June 2026)
        </div>
    </div>
    """
    
    # We find the start of the body content and inject the cover page
    if "<body>" in html_content:
        html_content = html_content.replace("<body>", f"<body>\n{cover_html}")
    else:
        # Fallback
        html_content = f"{cover_html}\n{html_content}"
        
    # Close the container div before the end of the body
    if "</body>" in html_content:
        html_content = html_content.replace("</body>", "</div>\n</body>")
    else:
        html_content = f"{html_content}\n</div>"
        
    with open(html_path, "w", encoding="utf-8") as f:
        f.write(html_content)
    print("HTML formatting and styling successfully injected.")
    
    # 4. Run MS Edge to print HTML to PDF
    print("Launching Microsoft Edge in headless mode to render PDF...")
    if not os.path.exists(edge_path):
        print(f"Error: MS Edge executable not found at {edge_path}")
        print("Please check your Windows Edge installation path.")
        sys.exit(1)
        
    edge_command = [
        edge_path,
        "--headless",
        f"--print-to-pdf={pdf_path}",
        "--no-pdf-header-footer",
        html_path
    ]
    
    try:
        subprocess.run(edge_command, check=True)
        print("Edge rendering process finished.")
    except subprocess.CalledProcessError as e:
        print(f"Error rendering PDF via Edge: {e}")
        sys.exit(1)
        
    # 5. Verify PDF creation and clean up intermediate HTML file
    if os.path.exists(pdf_path) and os.path.getsize(pdf_path) > 0:
        print("Success! PDF file created and validated successfully.")
        print(f"File location: {pdf_path}")
        print(f"File size: {os.path.getsize(pdf_path)} bytes")
        
        # Clean up temporary HTML file
        try:
            os.remove(html_path)
            print("Intermediate temporary HTML file cleaned up.")
        except OSError as e:
            print(f"Warning: Could not delete temporary HTML file: {e}")
    else:
        print("Error: PDF file generation failed (file is empty or missing).")
        sys.exit(1)

if __name__ == "__main__":
    main()
