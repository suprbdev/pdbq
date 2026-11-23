-- field: createEvent
WITH __mut AS (
  INSERT INTO "public"."events" ("name", "type_code")
  VALUES ($1, $2)
  RETURNING "id", "name", "type_code"
)
SELECT json_build_object('event', json_build_object('id', __t_1."id", 'name', __t_1."name", 'typeCode', CASE __t_1."type_code" WHEN 'conference' THEN 'CONFERENCE' WHEN 'hackathon' THEN 'HACKATHON' WHEN 'meetup' THEN 'MEETUP' END)) AS data
FROM __mut AS __t_1
-- args: ["PGConf","conference"]

