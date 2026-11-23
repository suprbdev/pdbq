-- field: userGreeting
SELECT to_json(__fn.v) AS data FROM (SELECT "public"."user_greeting"("greeting" => $1::"pg_catalog"."text", "u" => jsonb_populate_record(NULL::"public"."users", $2::jsonb)::"public"."users") AS v) AS __fn
-- args: ["Hello","{\"address\":{\"city\":\"London\"},\"email\":\"ada@example.com\",\"full_name\":\"Ada\",\"id\":1,\"mood\":\"happy\"}"]

-- field: totalTitleLength
SELECT to_json(__fn.v) AS data FROM (SELECT "public"."total_title_length"("ps" => coalesce((SELECT array_agg(jsonb_populate_record(NULL::"public"."posts", NULLIF(__e_1.v, 'null')) ORDER BY __e_1.o) FROM jsonb_array_elements($1::jsonb) WITH ORDINALITY AS __e_1(v, o)), ARRAY[]::"public"."posts"[])::"public"."_posts") AS v) AS __fn
-- args: ["[{\"title\":\"abc\"},null,{\"published\":true,\"title\":\"de\"}]"]

-- field: addressCities
SELECT to_json(__fn.v) AS data FROM (SELECT "public"."address_cities"("addrs" => coalesce((SELECT array_agg(jsonb_populate_record(NULL::"public"."address", NULLIF(__e_1.v, 'null')) ORDER BY __e_1.o) FROM jsonb_array_elements($1::jsonb) WITH ORDINALITY AS __e_1(v, o)), ARRAY[]::"public"."address"[])::"public"."_address") AS v) AS __fn
-- args: ["[{\"city\":\"Paris\"},null]"]

