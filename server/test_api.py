import urllib.request
import urllib.parse
import json
import subprocess
import time
import sys
import os

PORT = "8089"
BASE_URL = f"http://127.0.0.1:{PORT}"

env = os.environ.copy()
env["PORT"] = PORT
env["DB_PATH"] = "qaf.db"

exe_path = os.path.abspath("server/qaf-api.exe")
print(f"Starting {exe_path} on port {PORT}...")
proc = subprocess.Popen([exe_path], cwd="server", env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)

try:
    # Wait for server to start
    time.sleep(2)

    def test_get(endpoint):
        url = BASE_URL + endpoint
        req = urllib.request.Request(url, headers={"Accept-Encoding": "gzip"})
        with urllib.request.urlopen(req) as resp:
            headers = dict(resp.headers)
            body = resp.read()
            # If gzipped, decompress
            if headers.get("Content-Encoding") == "gzip":
                import gzip
                body = gzip.decompress(body)
            data = json.loads(body.decode("utf-8"))
            print(f"PASS [GET {endpoint}] -> status {resp.status}, content-encoding: {headers.get('Content-Encoding')}, cors: {headers.get('Access-Control-Allow-Origin')}")
            return data

    def test_post(endpoint, payload):
        url = BASE_URL + endpoint
        req_data = json.dumps(payload).encode("utf-8")
        req = urllib.request.Request(url, data=req_data, headers={"Content-Type": "application/json", "Accept-Encoding": "gzip"}, method="POST")
        with urllib.request.urlopen(req) as resp:
            headers = dict(resp.headers)
            body = resp.read()
            if headers.get("Content-Encoding") == "gzip":
                import gzip
                body = gzip.decompress(body)
            data = json.loads(body.decode("utf-8"))
            print(f"PASS [POST {endpoint}] -> status {resp.status}, cors: {headers.get('Access-Control-Allow-Origin')}")
            return data

    # 1. Health
    health = test_get("/api/health")
    assert health["status"] == "ok", f"Health status: {health}"

    # 2. Home
    home = test_get("/api/home")
    assert len(home["featured_poets"]) == 8, f"Featured poets count: {len(home['featured_poets'])}"
    assert len(home["featured_works"]) == 8, f"Featured works count: {len(home['featured_works'])}"
    print(f"  First poet: {home['featured_poets'][0]['name']}")
    print(f"  First work: {home['featured_works'][0]['title']} ({home['featured_works'][0]['poet_name']})")

    # 3. Filter Options
    filters = test_get("/api/filter-options")
    assert len(filters["poets"]) > 0, "No poets in filter options"
    assert len(filters["centuries"]) > 0, "No centuries in filter options"
    assert len(filters["cities"]) > 0, "No cities in filter options"
    print(f"  Poets: {len(filters['poets'])}, Centuries: {len(filters['centuries'])}, Cities: {len(filters['cities'])}")

    # 4. Poets list
    poets = test_get("/api/poets?century=8&limit=5")
    assert len(poets) > 0, "No poets returned for century 8"
    print(f"  Found {len(poets)} poets for century 8. First: {poets[0]['name']}")

    # 5. Poet detail
    poet2 = test_get("/api/poets/2")
    assert poet2["poet"]["id"] == 2, f"Poet ID mismatch: {poet2['poet']}"
    assert len(poet2["works"]) > 0, "No works for poet 2"
    print(f"  Poet 2: {poet2['poet']['name']}, Works count: {len(poet2['works'])}")

    # 6. Works list
    works = test_get("/api/works?century=8&limit=5")
    assert len(works) > 0, "No works returned"
    print(f"  Found {len(works)} works. First: {works[0]['title']}")

    # 7. Work detail
    work33 = test_get("/api/works/33")
    assert work33["work"]["id"] == 33, f"Work ID mismatch: {work33['work']}"
    print(f"  Work 33 text: {work33['work']['text']}, children: {len(work33['children'])}, poems: {len(work33['poems'])}")

    # If work 33 has children, check a leaf child
    leaf_id = work33["children"][0]["id"] if len(work33["children"]) > 0 else 33
    leaf_work = test_get(f"/api/works/{leaf_id}")
    print(f"  Leaf work {leaf_id}: children={len(leaf_work['children'])}, poems={len(leaf_work['poems'])}")
    if len(leaf_work["poems"]) > 0:
        print(f"    First poem: {leaf_work['poems'][0]['title']}, preview: {leaf_work['poems'][0].get('preview')}")

    # 8. Poems direct pagination
    work_poems = test_get(f"/api/works/{leaf_id}/poems?limit=3")
    assert len(work_poems) > 0, "No poems returned in direct pagination"
    print(f"  Direct pagination poems count: {len(work_poems)}")

    # 9. Poem detail
    first_poem_id = work_poems[0]["poem_id"]
    poem_detail = test_get(f"/api/poems/{first_poem_id}")
    assert poem_detail["poem"]["poem_id"] == first_poem_id, "Poem ID mismatch"
    assert len(poem_detail["verses"]) > 0, "No verses in poem detail"
    assert len(poem_detail["poem_ids"]) > 0, "No poem_ids sibling list"
    print(f"  Poem {first_poem_id}: '{poem_detail['poem']['title']}' has {len(poem_detail['verses'])} couplets/verses")
    print(f"    First couplet: {poem_detail['verses'][0]['items'][0]['text']}")

    # 10. Search across all scopes
    q_saadi = urllib.parse.quote("سعدی")
    search_res = test_get(f"/api/search?q={q_saadi}&scope=all")
    print(f"  Search 'سعدی' results: poets={len(search_res['poets'])}, works={len(search_res['works'])}, poems={len(search_res['poems'])}, verses={len(search_res['verses'])}")
    assert len(search_res["poets"]) > 0 or len(search_res["verses"]) > 0, "Search returned empty results"

    # Search verses specifically
    q_saghi = urllib.parse.quote("ساقی")
    verse_search = test_get(f"/api/search?q={q_saghi}&scope=verses")
    assert len(verse_search["verses"]) > 0, "No verse search results for ساقی"
    print(f"  Search verses 'ساقی': {len(verse_search['verses'])} verses returned")
    print(f"    First hit: {verse_search['verses'][0]['text']} ({verse_search['verses'][0]['poet_name']} - {verse_search['verses'][0]['title']})")

    # 11. Bookmarks batch resolver
    bm_res = test_post("/api/bookmarks", {
        "poets": [2, 3],
        "works": [33],
        "poems": [first_poem_id],
        "verses": [f"{first_poem_id}:1"]
    })
    assert len(bm_res["poets"]) == 2, f"Bookmarks poets mismatch: {len(bm_res['poets'])}"
    assert len(bm_res["works"]) == 1, f"Bookmarks works mismatch: {len(bm_res['works'])}"
    assert len(bm_res["poems"]) == 1, f"Bookmarks poems mismatch: {len(bm_res['poems'])}"
    assert len(bm_res["verses"]) > 0, f"Bookmarks verses mismatch: {len(bm_res['verses'])}"
    print(f"  Bookmarks resolved successfully: poets={len(bm_res['poets'])}, works={len(bm_res['works'])}, poems={len(bm_res['poems'])}, verses={len(bm_res['verses'])}")

    print("\nALL 11 API ENDPOINT TESTS PASSED SUCCESSFULLY!")

finally:
    proc.terminate()
    try:
        proc.wait(timeout=3)
    except:
        proc.kill()
    print("qaf-api.exe stopped.")
