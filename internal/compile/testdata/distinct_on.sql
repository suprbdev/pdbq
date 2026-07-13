-- field: allUsers
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_3.ndata ORDER BY __s_3.__rn ASC), '[]'::jsonb), 'edges', coalesce(jsonb_agg(jsonb_build_object('cursor', encode(convert_to('o:' || (__s_3.__rn + 0)::text, 'UTF8'), 'base64')) ORDER BY __s_3.__rn ASC), '[]'::jsonb), 'totalCount', (SELECT count(*) FROM (SELECT DISTINCT __c_4."mood" FROM "public"."users" AS __c_4) AS __c_5)::int, 'pageInfo', jsonb_build_object('hasNextPage', ((SELECT count(*) FROM (SELECT DISTINCT __c_4."mood" FROM "public"."users" AS __c_4) AS __c_5) > 0 + count(__s_3.__rn)), 'endCursor', (jsonb_agg(encode(convert_to('o:' || (__s_3.__rn + 0)::text, 'UTF8'), 'base64') ORDER BY __s_3.__rn ASC))->-1)) AS data
FROM (
  SELECT __d_2.*, row_number() OVER (ORDER BY __d_2.__rn0) AS __rn
  FROM (
    SELECT DISTINCT ON (__t_1."mood") jsonb_build_object('id', __t_1."id", 'mood', CASE __t_1."mood" WHEN 'happy' THEN 'HAPPY' WHEN 'ok' THEN 'OK' WHEN 'sad' THEN 'SAD' END) AS ndata, row_number() OVER (ORDER BY __t_1."mood" DESC, __t_1."id" ASC) AS __rn0
    FROM "public"."users" AS __t_1
    ORDER BY __t_1."mood" DESC, __t_1."id" ASC
    LIMIT $1
  ) AS __d_2
) AS __s_3
GROUP BY ()
-- args: [5]

